//go:build linux

package camerabench

import (
	"context"
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

func exerciseRTSPBlackhole(t *testing.T, ctx context.Context, client *http.Client, p *faultProxy) map[string]any {
	t.Helper()
	activeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	observed := make(chan time.Time, 128)
	done := make(chan rtspClientSample, 1)
	go func() {
		done <- measureRTSPClient(activeCtx, client, 1024, func(_ []byte, at time.Time) {
			select {
			case observed <- at:
			case <-activeCtx.Done():
			}
		})
	}()
	for i := 0; i < 8; i++ {
		select {
		case <-observed:
		case r := <-done:
			t.Fatalf("stream terminated before network fault: %v", r.err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	before := p.accepted.Load()
	faultAt := time.Now()
	p.drop.Store(true)
	defer p.drop.Store(false)
	hold := time.NewTimer(8 * time.Second)
	defer hold.Stop()
	finished := false
holdLoop:
	for {
		select {
		case at := <-observed:
			if at.Sub(faultAt) > 2*time.Second {
				t.Fatal("video continued despite silent network fault")
			}
		case <-done:
			finished = true
		case <-hold.C:
			break holdLoop
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	restored := time.Now()
	p.drop.Store(false)
	if p.dropped.Load() == 0 {
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
			case at := <-observed:
				if at.After(restored) {
					if n == 0 {
						first = at
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
	outcome := "not_recovered"
	if n >= 3 {
		outcome = "recovered"
	}
	report := map[string]any{"fault": "bidirectional silent TCP byte loss without intentional FIN/RST", "duration_ms": millisecond(restored.Sub(faultAt)), "dropped_bytes": p.dropped.Load(), "same_connection": outcome, "same_connection_frames_after_restore": n, "new_tcp_sessions": p.accepted.Load() - before}
	if !first.IsZero() {
		report["restore_to_first_frame_ms"] = millisecond(first.Sub(restored))
	}
	if n < 3 {
		t.Fatalf("existing HTTP subscriber failed to recover: %+v", report)
	}
	if p.accepted.Load() <= before {
		t.Fatal("no new RTSP session after timeout")
	}
	return report
}
