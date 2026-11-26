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
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestGo2RTCNativeSound exercises the actual server's activity API, native
// G.711 RTP subscription, and publisher loss. FFmpeg is only the independent
// synthetic source, never a child of go2rtc. No camera or production config.
func TestGo2RTCNativeSound(t *testing.T) { testGo2RTCSound(t, true) }
func TestGo2RTCLegacySound(t *testing.T) { testGo2RTCSound(t, false) }

func testGo2RTCSound(t *testing.T, native bool) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 130*time.Second)
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
		"webos_pip":      map[string]any{"enabled": true, "sound_enabled": true, "sound_native": native, "sound_stream": "source", "sound_threshold_dbfs": -32},
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
	source := "aevalsrc=if(lt(t\\,20)\\,0\\,0.25*sin(2*PI*440*t)):s=8000"
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
	poll("quiet PCMA", 19*time.Second, func() bool { return state() == "quiet" })
	quietMS := millisecond(time.Since(start))
	poll("active PCMA", 23*time.Second, func() bool { return state() == "active" })
	activeMS := millisecond(time.Since(start))
	profile := measureSoundSteady(t, ctx, server.Process.Pid, native, state)
	if err = pub.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-pubDone
	stopped = true
	lost := time.Now()
	poll("unavailable after publisher loss", 8*time.Second, func() bool { return state() == "unavailable" })
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
		return len(v.Cameras) == 1 && v.Cameras[0].ID == "source" && v.Cameras[0].Runtime.Consumers == 0 && v.Cameras[0].Runtime.Pending == 0 && len(transcodeChildren(server.Process.Pid)) == 0
	})
	// Republish a different G.711 codec after the previous consumer was
	// detached. The monitor must reattach without a server restart.
	pub = exec.CommandContext(ctx, ffmpeg, "-nostdin", "-hide_banner", "-loglevel", "error", "-re", "-f", "lavfi", "-i", "aevalsrc=0.25*sin(2*PI*440*t):s=8000", "-c:a", "pcm_mulaw", "-ar", "8000", "-ac", "1", "-vn", "-rtsp_transport", "tcp", "-f", "rtsp", "rtsp://"+address+"/source")
	pub.Stdout, pub.Stderr = io.Discard, io.Discard
	restarted := time.Now()
	if err = pub.Start(); err != nil {
		t.Fatal(err)
	}
	pubDone = make(chan error, 1)
	stopped = false
	go func(cmd *exec.Cmd, done chan error) { done <- cmd.Wait() }(pub, pubDone)
	poll("active PCMU after republish", 20*time.Second, func() bool { return state() == "active" })
	recoveredMS := millisecond(time.Since(restarted))
	if err = pub.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-pubDone
	stopped = true
	poll("unavailable after second publisher loss", 8*time.Second, func() bool { return state() == "unavailable" })
	poll("no remaining subscription after republish", 18*time.Second, func() bool {
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
		return len(v.Cameras) == 1 && v.Cameras[0].ID == "source" && v.Cameras[0].Runtime.Consumers == 0 && v.Cameras[0].Runtime.Pending == 0 && len(transcodeChildren(server.Process.Pid)) == 0
	})
	wantChildren := 1
	mode := "legacy"
	if native {
		wantChildren = 0
		mode = "native"
	}
	if maxChildren != wantChildren || len(transcodeChildren(server.Process.Pid)) != 0 {
		t.Fatalf("unexpected %s FFmpeg process count: peak=%d", mode, maxChildren)
	}
	report := map[string]any{"version": "s3-sound-comparison-v3", "mode": mode, "steady_active_profile": profile, "codec": "PCMA/8000", "replacement_codec": "PCMU/8000", "restart_to_active_ms": recoveredMS, "physical_camera": false, "quiet_ms": quietMS, "active_ms": activeMS, "unavailable_after_loss_ms": unavailableMS, "server_ffmpeg_children_peak_sampled": maxChildren, "remaining_consumers": 0, "remaining_pending": 0}
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

func measureSoundSteady(t *testing.T, ctx context.Context, pid int, native bool, state func() string) map[string]any {
	t.Helper()
	raw, err := exec.CommandContext(ctx, "getconf", "CLK_TCK").Output()
	if err != nil {
		t.Fatal(err)
	}
	ticks, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil || ticks <= 0 {
		t.Fatal("invalid clock ticks")
	}
	proc := func(id int) ffmpegProcSample {
		root := fmt.Sprintf("/proc/%d/", id)
		raw, err := os.ReadFile(root + "stat")
		if err != nil {
			t.Fatal(err)
		}
		end := strings.LastIndexByte(string(raw), ')')
		if end < 0 {
			t.Fatal("invalid process stat")
		}
		fields := strings.Fields(string(raw[end+1:]))
		if len(fields) < 13 {
			t.Fatal("short process stat")
		}
		u, e1 := strconv.ParseInt(fields[11], 10, 64)
		s, e2 := strconv.ParseInt(fields[12], 10, 64)
		if e1 != nil || e2 != nil {
			t.Fatal("invalid CPU counters")
		}
		status, err := os.ReadFile(root + "status")
		if err != nil {
			t.Fatal(err)
		}
		var rss int64
		for _, line := range strings.Split(string(status), "\n") {
			f := strings.Fields(line)
			if len(f) >= 2 && f[0] == "VmHWM:" {
				rss, _ = strconv.ParseInt(f[1], 10, 64)
			}
		}
		if rss <= 0 {
			t.Fatal("missing process VmHWM")
		}
		return ffmpegProcSample{u, s, rss}
	}
	before := proc(pid)
	children := transcodeChildren(pid)
	childID := 0
	var childBefore ffmpegProcSample
	if native {
		if len(children) != 0 {
			t.Fatal("native mode has FFmpeg child")
		}
	} else {
		if len(children) != 1 {
			t.Fatal("legacy mode missing unique FFmpeg child")
		}
		for id, s := range children {
			childID, childBefore = id, s
		}
	}
	start := time.Now()
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	samples := 0
	done := false
	for !done {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-deadline.C:
			done = true
		case <-ticker.C:
			if state() != "active" {
				t.Fatal("activity lost in stable tone window")
			}
			c := transcodeChildren(pid)
			if native && len(c) != 0 {
				t.Fatal("unexpected native child")
			}
			if !native {
				if _, ok := c[childID]; len(c) != 1 || !ok {
					t.Fatal("legacy child replaced during steady window")
				}
			}
			samples++
		}
	}
	after := proc(pid)
	var childAfter ffmpegProcSample
	if !native {
		childAfter = proc(childID)
	}
	ms := func(v int64) float64 {
		if v < 0 {
			t.Fatal("nonmonotonic process counter")
		}
		return float64(v) * 1000 / float64(ticks)
	}
	return map[string]any{
		"duration_ms":          millisecond(time.Since(start)),
		"activity_samples":     samples,
		"go2rtc_cpu_user_ms":   ms(after.CPUUserTicks - before.CPUUserTicks),
		"go2rtc_cpu_system_ms": ms(after.CPUSystemTicks - before.CPUSystemTicks),
		"go2rtc_vmhwm_kib":     after.PeakRSSKiB,
		"ffmpeg_cpu_user_ms":   ms(childAfter.CPUUserTicks - childBefore.CPUUserTicks),
		"ffmpeg_cpu_system_ms": ms(childAfter.CPUSystemTicks - childBefore.CPUSystemTicks),
		"ffmpeg_vmhwm_kib":     childAfter.PeakRSSKiB,
		"memory_scope":         "process-lifetime VmHWM sampled during steady active window",
	}
}
