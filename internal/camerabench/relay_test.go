package camerabench

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// TestGo2RTCRelayBaseline is opt-in: it runs the freshly built go2rtc binary
// with an explicit temporary configuration and a UNIX socket. Its only
// source is a local HTTP fixture replaying bounded FFmpeg-generated JPEGs.
func TestGo2RTCRelayBaseline(t *testing.T) {
	binary := os.Getenv("GO2RTC_BENCH_BINARY")
	if binary == "" {
		t.Skip("requires explicitly built go2rtc binary")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	frames, ff, err := runFFmpeg(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int64
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=sample")
		w.(http.Flusher).Flush()
		timer := time.NewTicker(time.Second / sampleFPS)
		defer timer.Stop()
		for i := 0; i < 128; i++ {
			select {
			case <-r.Context().Done():
				return
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			frame := frames[i%len(frames)]
			if _, err := fmt.Fprintf(w, "--sample\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", len(frame)); err != nil {
				return
			}
			if _, err := w.Write(frame); err != nil {
				return
			}
			if _, err := io.WriteString(w, "\r\n"); err != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
	}))
	defer source.Close()

	// A fresh, private UNIX socket avoids port races and accidental access
	// to an existing service. All network modules except HTTP/MJPEG are off.
	dir, err := os.MkdirTemp("", "g2bench-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "api.sock")
	config, err := json.Marshal(map[string]any{
		"app":            map[string]any{"modules": []string{"api", "http", "mjpeg", "camera_metrics"}},
		"api":            map[string]any{"listen": "", "unix_listen": socket},
		"streams":        map[string]string{"bench": source.URL},
		"camera_metrics": map[string]bool{"enabled": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, binary, "-c", string(config))
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	stopped := false
	defer func() {
		if !stopped {
			_ = cmd.Process.Kill()
			<-wait
		}
	}()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 8 * time.Second}
	type counts struct {
		Consumers int `json:"consumers"`
		Pending   int `json:"pending_subscriptions"`
	}
	readCounts := func() (counts, error) {
		var snapshot struct {
			Cameras []struct {
				ID      string `json:"id"`
				Runtime counts `json:"runtime"`
			} `json:"cameras"`
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://bench/api/cameras/metrics", nil)
		if err != nil {
			return counts{}, err
		}
		res, err := client.Do(req)
		if err != nil {
			return counts{}, err
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return counts{}, fmt.Errorf("metrics HTTP %d", res.StatusCode)
		}
		if err := json.NewDecoder(io.LimitReader(res.Body, 65536)).Decode(&snapshot); err != nil {
			return counts{}, err
		}
		if len(snapshot.Cameras) != 1 || snapshot.Cameras[0].ID != "bench" {
			return counts{}, fmt.Errorf("unexpected isolated registry")
		}
		return snapshot.Cameras[0].Runtime, nil
	}
	pollCounts := func(want int) error {
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			c, err := readCounts()
			if err == nil && c.Consumers == want && c.Pending == 0 {
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(25 * time.Millisecond):
			}
		}
		return fmt.Errorf("consumer count %d not observed", want)
	}
	if err := pollCounts(0); err != nil {
		t.Fatal(err)
	}
	type clientReceipt struct {
		FirstFrameMS float64 `json:"first_frame_ms"`
		FPS          float64 `json:"fps"`
		Frames       int     `json:"frames"`
		err          error
	}
	runClient := func() (out clientReceipt) {
		start := time.Now()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://bench/api/stream.mjpeg?src=bench", nil)
		if err != nil {
			out.err = err
			return
		}
		res, err := client.Do(req)
		if err != nil {
			out.err = err
			return
		}
		defer res.Body.Close()
		typ, params, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
		if err != nil || typ != "multipart/x-mixed-replace" || params["boundary"] == "" || res.StatusCode != http.StatusOK {
			out.err = fmt.Errorf("unexpected MJPEG response")
			return
		}
		reader := multipart.NewReader(res.Body, params["boundary"])
		var first, last time.Time
		for i := 0; i < 16; i++ {
			part, err := reader.NextPart()
			if err != nil {
				out.err = err
				return
			}
			data, err := io.ReadAll(io.LimitReader(part, maxJPEGBytes+1))
			if err != nil || len(data) > maxJPEGBytes || !bytes.HasPrefix(data, []byte{0xff, 0xd8}) || !bytes.HasSuffix(data, []byte{0xff, 0xd9}) {
				out.err = fmt.Errorf("invalid relayed JPEG")
				return
			}
			now := time.Now()
			if i == 0 {
				first = now
				out.FirstFrameMS = millisecond(now.Sub(start))
			}
			last = now
			out.Frames++
		}
		if !last.After(first) {
			out.err = fmt.Errorf("nonpositive sample duration")
			return
		}
		out.FPS = float64(out.Frames-1) / last.Sub(first).Seconds()
		return
	}
	results := make(chan clientReceipt, 2)
	for i := 0; i < 2; i++ {
		go func() { results <- runClient() }()
	}
	if err := pollCounts(2); err != nil {
		t.Fatal(err)
	}
	a, b := <-results, <-results
	if a.err != nil || b.err != nil {
		t.Fatalf("relay clients: %v; %v", a.err, b.err)
	}
	if err := pollCounts(0); err != nil {
		t.Fatal(err)
	}
	connections := requests.Load()
	if connections != 1 {
		t.Fatalf("expected shared source, got %d HTTP connections", connections)
	}
	// Reconnect a new consumer after the two previous clients detached.
	c := runClient()
	if c.err != nil {
		t.Fatal(c.err)
	}
	if err := pollCounts(0); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	err = <-wait
	stopped = true
	if err != nil {
		t.Fatal(err)
	}
	// Resource counters are process lifetime, not per-consumer CPU or
	// encoder CPU inside go2rtc. The FFmpeg generator ran separately.
	report := map[string]any{
		"version":                   "s2-go2rtc-relay-v1",
		"sampled_at":                time.Now().UTC().Format(time.RFC3339),
		"go2rtc_mjpeg_relay":        true,
		"physical_camera":           false,
		"rtsp_transcode":            false,
		"ffmpeg_fixture_generation": ff,
		"clients":                   []clientReceipt{a, b},
		"reconnect_client":          c,
		"shared_source_connections": connections,
		"remaining_consumers":       0,
		"go2rtc_process_user_ms":    millisecond(cmd.ProcessState.UserTime()),
		"go2rtc_process_system_ms":  millisecond(cmd.ProcessState.SystemTime()),
	}
	if rss, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok {
		report["go2rtc_peak_rss_kib"] = rss.Maxrss
	} else {
		t.Fatal("go2rtc resource receipt unavailable")
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(data))
	if output := os.Getenv("GO2RTC_BENCH_REPORT"); output != "" {
		if err := os.WriteFile(output, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
