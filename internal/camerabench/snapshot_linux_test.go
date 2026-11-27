//go:build linux

package camerabench

import (
	"bytes"
	"context"
	"fmt"
	"image/jpeg"
	"io"
	"net/http"
	"testing"
	"time"
)

// Exercise real snapshot conversion concurrently with a shared live MJPEG
// subscription. The publisher belongs to the enclosing isolated RTSP fixture.
func exerciseSnapshotLoad(t *testing.T, ctx context.Context, client *http.Client, pid int) map[string]any {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	first := make(chan struct{}, 1)
	live := make(chan rtspClientSample, 1)
	go func() {
		live <- measureRTSPClient(ctx, client, 112, func(_ []byte, _ time.Time) {
			select {
			case first <- struct{}{}:
			default:
			}
		})
	}()
	select {
	case <-first:
	case r := <-live:
		t.Fatalf("live stream stopped before burst: %v", r.err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	type sample struct {
		status int
		data   []byte
		err    error
	}
	get := func(query string) sample {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://bench/api/frame.jpeg?src=source"+query, nil)
		if err != nil {
			return sample{err: err}
		}
		res, err := client.Do(req)
		if err != nil {
			return sample{err: err}
		}
		defer res.Body.Close()
		b, err := io.ReadAll(io.LimitReader(res.Body, (16<<20)+1))
		if err != nil {
			return sample{status: res.StatusCode, err: err}
		}
		if res.StatusCode == 200 {
			if len(b) > 16<<20 {
				return sample{err: fmt.Errorf("oversized JPEG response")}
			}
			img, err := jpeg.Decode(bytes.NewReader(b))
			if err != nil {
				return sample{err: err}
			}
			if img.Bounds().Dx() != 80 || img.Bounds().Dy() != 48 {
				return sample{err: fmt.Errorf("unexpected JPEG transform dimensions")}
			}
		} else if res.StatusCode == 503 && res.Header.Get("Retry-After") != "1" {
			return sample{err: fmt.Errorf("missing Retry-After")}
		}
		return sample{status: res.StatusCode, data: b}
	}
	const burst = 16
	ready := make(chan struct{}, burst)
	start := make(chan struct{})
	results := make(chan sample, burst)
	for i := 0; i < burst; i++ {
		go func() { ready <- struct{}{}; <-start; results <- get("&width=80&height=48") }()
	}
	for i := 0; i < burst; i++ {
		<-ready
	}
	close(start)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	ok, busy, peakChildren, observations := 0, 0, 0, 0
	var peakRSS int64
	sampleChildren := func() {
		children := transcodeChildren(pid)
		observations++
		if len(children) > peakChildren {
			peakChildren = len(children)
		}
		var sum int64
		for _, s := range children {
			sum += s.PeakRSSKiB
		}
		if sum > peakRSS {
			peakRSS = sum
		}
		if len(children) > 3 {
			t.Fatalf("shared live transcoder plus at most two snapshots: got %d", len(children))
		}
	}
	for i := 0; i < burst; {
		select {
		case r := <-results:
			if r.err != nil {
				t.Fatal(r.err)
			}
			switch r.status {
			case 200:
				ok++
			case 503:
				busy++
			default:
				t.Fatalf("snapshot status %d", r.status)
			}
			i++
		case <-ticker.C:
			sampleChildren()
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if ok == 0 || busy == 0 {
		t.Fatalf("expected useful output and overload rejection: %d %d", ok, busy)
	}
	// Cache hits are checked byte-for-byte, and a separate unit test proves
	// that cache readback needs no producer or acquisition slot.
	a := get("&width=80&height=48&cache=30s")
	b := get("&w=80&h=48&cache=30s")
	if a.err != nil || b.err != nil || a.status != 200 || b.status != 200 || !bytes.Equal(a.data, b.data) {
		t.Fatalf("cached snapshot mismatch: %d %d %v %v", a.status, b.status, a.err, b.err)
	}
	var out rtspClientSample
	for {
		select {
		case out = <-live:
			if out.err != nil || out.Frames != 112 || out.FPS < 6 {
				t.Fatalf("live stream degraded during snapshot load: %+v", out)
			}
			return map[string]any{"burst_requests": burst, "success": ok, "busy": busy, "live_frames": out.Frames, "live_fps": out.FPS, "sampled_ffmpeg_children_peak": peakChildren, "sampled_sum_concurrent_child_vmhwm_kib": peakRSS, "process_samples": observations, "cache_hit_equal": true}
		case <-ticker.C:
			sampleChildren()
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}
