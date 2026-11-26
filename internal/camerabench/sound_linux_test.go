//go:build linux

package camerabench

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestGo2RTCNativeSound exercises the actual server's activity API, native
// G.711 RTP subscription, and publisher loss. FFmpeg is only the independent
// synthetic source, never a child of go2rtc. No camera or production config.
func TestGo2RTCNativeSound(t *testing.T) {
	binary := os.Getenv("GO2RTC_BENCH_BINARY")
	if binary == "" {
		t.Skip("requires explicitly built go2rtc binary")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp("", "g2sound-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "api.sock")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := ln.Addr().String()
	cfg, err := json.Marshal(map[string]any{
		"app":            map[string]any{"modules": []string{"api", "rtsp", "webos_pip", "camera_metrics"}},
		"api":            map[string]any{"listen": "", "unix_listen": socket},
		"rtsp":           map[string]any{"listen": address},
		"streams":        map[string]any{"source": nil},
		"webos_pip":      map[string]any{"enabled": true, "sound_enabled": true, "sound_native": true, "sound_stream": "source", "sound_threshold_dbfs": -32},
		"camera_metrics": map[string]bool{"enabled": true},
	})
	_ = ln.Close()
	if err != nil {
		t.Fatal(err)
	}
	server := exec.CommandContext(ctx, binary, "-c", string(cfg))
	server.Dir = dir
	server.Stdout, server.Stderr = io.Discard, io.Discard
	if err = server.Start(); err != nil {
		t.Fatal(err)
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Wait() }()
	defer func() { _ = server.Process.Kill(); <-serverDone }()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	readJSON := func(path string, out any) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://bench"+path, nil)
		if err != nil {
			return err
		}
		res, err := client.Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			return fmt.Errorf("HTTP %d", res.StatusCode)
		}
		return json.NewDecoder(io.LimitReader(res.Body, 65536)).Decode(out)
	}
	var maxChildren int
	state := func() string {
		var v struct{ State string }
		if err := readJSON("/api/webos-pip/activity", &v); err != nil {
			return ""
		}
		if n := len(transcodeChildren(server.Process.Pid)); n > maxChildren {
			maxChildren = n
		}
		return v.State
	}
	poll := func(label string, timeout time.Duration, want func() bool) {
		t.Helper()
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			if want() {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(100 * time.Millisecond):
			}
		}
		t.Fatalf("did not observe %s", label)
	}
	poll("initial unavailable", 8*time.Second, func() bool { return state() == "unavailable" })
	// The native monitor initially retries after an absent producer; retain
	// silence long enough to observe quiet before generating a loud tone.
	source := "aevalsrc=if(lt(t\\,12)\\,0\\,0.25*sin(2*PI*440*t)):s=8000"
	pub := exec.CommandContext(ctx, ffmpeg, "-nostdin", "-hide_banner", "-loglevel", "error", "-re", "-f", "lavfi", "-i", source, "-c:a", "pcm_alaw", "-ar", "8000", "-ac", "1", "-vn", "-rtsp_transport", "tcp", "-f", "rtsp", "rtsp://"+address+"/source")
	pub.Stdout, pub.Stderr = io.Discard, io.Discard
	if err = pub.Start(); err != nil {
		t.Fatal(err)
	}
	pubDone := make(chan error, 1)
	go func() { pubDone <- pub.Wait() }()
	stopped := false
	defer func() {
		if !stopped {
			_ = pub.Process.Kill()
			<-pubDone
		}
	}()
	start := time.Now()
	poll("quiet PCMA", 11*time.Second, func() bool { return state() == "quiet" })
	quietMS := millisecond(time.Since(start))
	poll("active PCMA", 12*time.Second, func() bool { return state() == "active" })
	activeMS := millisecond(time.Since(start))
	if err = pub.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-pubDone
	stopped = true
	lost := time.Now()
	poll("unavailable after publisher loss", 6*time.Second, func() bool { return state() == "unavailable" })
	unavailableMS := millisecond(time.Since(lost))
	poll("no remaining subscription", 18*time.Second, func() bool {
		var v struct {
			Cameras []struct {
				ID      string
				Runtime struct {
					Consumers int
					Pending   int `json:"pending_subscriptions"`
				}
			}
		}
		if err := readJSON("/api/cameras/metrics", &v); err != nil {
			return false
		}
		return len(v.Cameras) == 1 && v.Cameras[0].ID == "source" && v.Cameras[0].Runtime.Consumers == 0 && v.Cameras[0].Runtime.Pending == 0
	})
	if maxChildren != 0 || len(transcodeChildren(server.Process.Pid)) != 0 {
		t.Fatal("server spawned an FFmpeg process in native mode")
	}
	report := map[string]any{"version": "g8-native-sound-v1", "codec": "PCMA/8000", "physical_camera": false, "quiet_ms": quietMS, "active_ms": activeMS, "unavailable_after_loss_ms": unavailableMS, "server_ffmpeg_children_peak_sampled": maxChildren, "remaining_consumers": 0, "remaining_pending": 0}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(data))
	if out := os.Getenv("GO2RTC_SOUND_REPORT"); out != "" {
		if err = os.WriteFile(out, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
