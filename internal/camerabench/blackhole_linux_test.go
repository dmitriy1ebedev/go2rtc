//go:build linux

package camerabench

import (
	"bytes"
	"context"
	"fmt"
	"image/jpeg"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Deliberately discard traffic in both directions without FIN/RST. The
// listener and publisher stay alive. This models a silent network outage,
// not RF interference, packet reordering, or a real Wi-Fi device.
type faultProxy struct {
	ln       net.Listener
	target   string
	drop     atomic.Bool
	dropped  atomic.Uint64
	accepted atomic.Uint64
	mu       sync.Mutex
	closed   bool
	conns    map[net.Conn]struct{}
	wg       sync.WaitGroup
}

func newFaultProxy(t *testing.T, target string) *faultProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &faultProxy{ln: ln, target: target, conns: map[net.Conn]struct{}{}}
	p.wg.Add(1)
	go p.serve()
	t.Cleanup(p.close)
	return p
}

type faultWriter struct {
	p   *faultProxy
	dst net.Conn
}

func (w faultWriter) Write(b []byte) (int, error) {
	if w.p.drop.Load() {
		w.p.dropped.Add(uint64(len(b)))
		return len(b), nil
	}
	return w.dst.Write(b)
}

func (p *faultProxy) serve() {
	defer p.wg.Done()
	for {
		a, err := p.ln.Accept()
		if err != nil {
			return
		}
		b, err := net.DialTimeout("tcp", p.target, time.Second)
		if err != nil {
			_ = a.Close()
			continue
		}
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			_ = a.Close()
			_ = b.Close()
			return
		}
		p.conns[a] = struct{}{}
		p.conns[b] = struct{}{}
		p.accepted.Add(1)
		p.wg.Add(1)
		p.mu.Unlock()
		go func() {
			defer p.wg.Done()
			done := make(chan struct{})
			go func() { _, _ = io.Copy(faultWriter{p, b}, a); _ = a.Close(); _ = b.Close(); close(done) }()
			_, _ = io.Copy(faultWriter{p, a}, b)
			_ = a.Close()
			_ = b.Close()
			<-done
			p.mu.Lock()
			delete(p.conns, a)
			delete(p.conns, b)
			p.mu.Unlock()
		}()
	}
}

func (p *faultProxy) close() {
	_ = p.ln.Close()
	p.mu.Lock()
	p.closed = true
	for c := range p.conns {
		_ = c.Close()
	}
	p.mu.Unlock()
	p.wg.Wait()
}

func (p *faultProxy) active() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.conns) / 2
}

type blackholeObservation struct {
	seq      int
	sent, at time.Time
	err      error
}

// Decode the source marker rather than mistaking delayed buffered JPEGs for
// live traffic. The marker clock belongs to this process and is monotonic.
func blackholeFrame(f *timedFixture, data []byte, at time.Time) blackholeObservation {
	o := blackholeObservation{at: at}
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		o.err = err
		return o
	}
	o.seq, o.err = markerSequence(img)
	if o.err != nil {
		return o
	}
	f.mu.Lock()
	o.sent = f.sent[o.seq]
	f.mu.Unlock()
	if o.sent.IsZero() || o.sent.After(o.at) {
		o.err = fmt.Errorf("invalid source timestamp for marker %d", o.seq)
	}
	return o
}

func exerciseRTSPBlackhole(t *testing.T, ctx context.Context, client *http.Client, p *faultProxy, f *timedFixture) map[string]any {
	t.Helper()
	activeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	observed := make(chan blackholeObservation, 128)
	done := make(chan rtspClientSample, 1)
	go func() {
		done <- measureRTSPClient(activeCtx, client, 1024, func(data []byte, at time.Time) {
			o := blackholeFrame(f, data, at)
			select {
			case observed <- o:
			case <-activeCtx.Done():
			}
		})
	}()
	lastSeq := 0
	for n := 0; n < 8; {
		select {
		case o := <-observed:
			if o.err != nil {
				t.Fatalf("invalid marker before fault after %d valid frames: %v", n, o.err)
			}
			if o.seq > lastSeq {
				lastSeq = o.seq
				n++
			}
		case r := <-done:
			t.Fatalf("stream terminated before network fault: %v", r.err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	before := p.accepted.Load()
	if before == 0 || p.active() == 0 {
		t.Fatal("no live RTSP session through the fault proxy")
	}
	beforeDropped := p.dropped.Load()
	p.drop.Store(true)
	faultAt := time.Now()
	defer p.drop.Store(false)
	hold := time.NewTimer(30 * time.Second)
	defer hold.Stop()
	check := time.NewTicker(100 * time.Millisecond)
	defer check.Stop()
	finished := false
	lastBuffered := faultAt
	buffered := 0
	invalidBlocked, invalidAfter := 0, 0
	lastMarkerError := ""
	// A small boundary allowance covers a write already in flight at the
	// fault edge. Frames generated later must not traverse the blocked path.
	observeBlocked := func(o blackholeObservation) {
		// An interleaved RTSP packet can be cut in the middle at the
		// fault boundary. Count undecodable markers as delivered images
		// for the silence criterion, never as verified fresh frames.
		if o.err != nil {
			invalidBlocked++
			lastMarkerError = o.err.Error()
		} else if o.sent.After(faultAt.Add(250 * time.Millisecond)) {
			t.Fatalf("fresh source marker %d crossed blocked path: generated %v after fault, received %v after fault", o.seq, o.sent.Sub(faultAt), o.at.Sub(faultAt))
		}
		if o.at.After(faultAt) {
			buffered++
			if o.at.After(lastBuffered) {
				lastBuffered = o.at
			}
		}
	}
holdLoop:
	for {
		select {
		case o := <-observed:
			observeBlocked(o)
		case <-done:
			finished = true
		case <-check.C:
			// A short blackout can recover on the original TCP session.
			// Keep the fault active until a real replacement dial is seen,
			// bounded by the hard deadline above.
			if time.Since(faultAt) >= 8*time.Second && p.accepted.Load() > before && time.Since(lastBuffered) >= 2*time.Second {
				break holdLoop
			}
		case <-hold.C:
			break holdLoop
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	sessionsDuringFault := p.accepted.Load() - before
	p.drop.Store(false)
	restored := time.Now()
	dropped := p.dropped.Load() - beforeDropped
	if dropped == 0 {
		t.Fatal("fault did not discard traffic")
	}
	var first time.Time
	n := 0
	timer := time.NewTimer(25 * time.Second)
	defer timer.Stop()
	if !finished {
	observe:
		for n < 3 {
			select {
			case o := <-observed:
				if !o.at.After(restored) {
					observeBlocked(o)
					continue
				}
				if o.err != nil {
					invalidAfter++
					lastMarkerError = o.err.Error()
					n = 0
					first = time.Time{}
					continue
				}
				// Require new, increasing source markers produced after
				// restoration, not a replay of previously buffered frames.
				if o.sent.After(restored) && o.seq > lastSeq {
					lastSeq = o.seq
					if n == 0 {
						first = o.at
					}
					n++
				}
			case <-done:
				finished = true
				break observe
			case <-timer.C:
				break observe
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
	}
	cancel()
	if !finished {
		select {
		case <-done:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	// Include any observations already queued at the restoration edge.
	for {
		select {
		case o := <-observed:
			if !o.at.After(restored) {
				observeBlocked(o)
			}
		default:
			goto drained
		}
	}
drained:
	silence := restored.Sub(lastBuffered)
	outcome := "not_recovered"
	if n >= 3 {
		outcome = "recovered"
	}
	report := map[string]any{"fault": "bidirectional silent TCP byte loss without intentional FIN/RST", "duration_ms": millisecond(restored.Sub(faultAt)), "dropped_bytes": dropped, "buffered_frames_during_fault": buffered, "invalid_markers_during_fault": invalidBlocked, "invalid_markers_after_restore": invalidAfter, "last_marker_error": lastMarkerError, "buffer_drain_ms": millisecond(lastBuffered.Sub(faultAt)), "silence_before_restore_ms": millisecond(silence), "same_connection": outcome, "same_connection_frames_after_restore": n, "fresh_source_markers_verified": n >= 3, "new_tcp_sessions": p.accepted.Load() - before, "new_tcp_sessions_during_fault": sessionsDuringFault}
	if !first.IsZero() {
		report["restore_to_first_frame_ms"] = millisecond(first.Sub(restored))
	}
	if silence < 2*time.Second {
		t.Fatalf("no sustained video blackout: %+v", report)
	}
	if n < 3 {
		t.Fatalf("existing HTTP subscriber failed to recover fresh frames: %+v", report)
	}
	if p.accepted.Load() <= before {
		t.Fatalf("no new RTSP session after timeout: %+v", report)
	}
	return report
}
