//go:build linux

package camerabench

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

type rtspClientSample struct {
	FirstFrameMS         float64 `json:"first_frame_ms"`
	FPS                  float64 `json:"fps"`
	PayloadBitsPerSecond float64 `json:"jpeg_payload_bits_per_second"`
	Frames               int     `json:"frames"`
	err                  error
}

func measureRTSPClient(ctx context.Context, client *http.Client, frames int, observe ...func([]byte, time.Time)) (out rtspClientSample) {
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
	if err != nil || typ != "multipart/x-mixed-replace" || params["boundary"] == "" || res.StatusCode != 200 {
		out.err = fmt.Errorf("unexpected MJPEG HTTP %d", res.StatusCode)
		return
	}
	reader := multipart.NewReader(res.Body, params["boundary"])
	var first, last time.Time
	var payload int64
	for i := 0; i < frames; i++ {
		part, err := reader.NextPart()
		if err != nil {
			out.err = err
			return
		}
		data, err := io.ReadAll(io.LimitReader(part, maxJPEGBytes+1))
		if err != nil || len(data) > maxJPEGBytes || !bytes.HasPrefix(data, []byte{0xff, 0xd8}) || !bytes.HasSuffix(data, []byte{0xff, 0xd9}) {
			out.err = fmt.Errorf("invalid transcoded JPEG")
			return
		}
		now := time.Now()
		if i == 0 {
			first = now
			out.FirstFrameMS = millisecond(now.Sub(start))
		} else {
			payload += int64(len(data))
		}
		last = now
		out.Frames++
		for _, fn := range observe {
			fn(data, now)
		}
	}
	span := last.Sub(first).Seconds()
	if span <= 0 {
		out.err = fmt.Errorf("nonpositive frame span")
		return
	}
	out.FPS = float64(frames-1) / span
	out.PayloadBitsPerSecond = float64(payload*8) / span
	return
}

type ffmpegProcSample struct {
	CPUUserTicks   int64 `json:"cpu_user_ticks"`
	CPUSystemTicks int64 `json:"cpu_system_ticks"`
	PeakRSSKiB     int64 `json:"peak_rss_kib"`
}

// Linux exposes direct children of each thread. Read only processes owned by
// this isolated server, and sample cumulative CPU ticks and VmHWM while alive.
func transcodeChildren(pid int) map[int]ffmpegProcSample {
	out := make(map[int]ffmpegProcSample)
	tasks, _ := os.ReadDir(fmt.Sprintf("/proc/%d/task", pid))
	for _, task := range tasks {
		children, _ := os.ReadFile(fmt.Sprintf("/proc/%d/task/%s/children", pid, task.Name()))
		for _, child := range strings.Fields(string(children)) {
			id, err := strconv.Atoi(child)
			if err != nil || id <= 0 {
				continue
			}
			root := fmt.Sprintf("/proc/%d/", id)
			comm, err := os.ReadFile(root + "comm")
			if err != nil || strings.TrimSpace(string(comm)) != "ffmpeg" {
				continue
			}
			raw, err := os.ReadFile(root + "stat")
			if err != nil {
				continue
			}
			end := strings.LastIndexByte(string(raw), ')')
			if end < 0 {
				continue
			}
			fields := strings.Fields(string(raw[end+1:]))
			if len(fields) < 13 {
				continue
			}
			u, e1 := strconv.ParseInt(fields[11], 10, 64)
			s, e2 := strconv.ParseInt(fields[12], 10, 64)
			if e1 != nil || e2 != nil {
				continue
			}
			status, err := os.ReadFile(root + "status")
			if err != nil {
				continue
			}
			var hwm int64
			for _, line := range strings.Split(string(status), "\n") {
				f := strings.Fields(line)
				if len(f) >= 2 && f[0] == "VmHWM:" {
					hwm, _ = strconv.ParseInt(f[1], 10, 64)
				}
			}
			if hwm > 0 {
				out[id] = ffmpegProcSample{u, s, hwm}
			}
		}
	}
	return out
}

// TestGo2RTCRTSPBaseline exercises a real H264 RTSP publisher, the go2rtc
// RTSP server, its normal FFmpeg source, and two concurrent MJPEG API clients.
// No production config, physical camera or external network is used.
func TestGo2RTCRTSPBaseline(t *testing.T) {
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
	ticksRaw, err := exec.Command("getconf", "CLK_TCK").Output()
	if err != nil {
		t.Fatal(err)
	}
	ticks, err := strconv.Atoi(strings.TrimSpace(string(ticksRaw)))
	if err != nil || ticks <= 0 {
		t.Fatal("invalid CLK_TCK")
	}
	outage := os.Getenv("GO2RTC_RTSP_OUTAGE") == "1"
	budget := 60 * time.Second
	if outage {
		budget = 100 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	dir, err := os.MkdirTemp("", "g2rtsp-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "api.sock")
	// Reserve an isolated loopback address. The tiny release/bind interval
	// is checked by readiness, and the fixture never targets a public host.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := ln.Addr().String()
	profile := "default"
	configValues := map[string]any{
		"app":            map[string]any{"modules": []string{"api", "rtsp", "exec", "ffmpeg", "mjpeg", "camera_metrics"}},
		"api":            map[string]any{"listen": "", "unix_listen": socket},
		"rtsp":           map[string]any{"listen": address},
		"streams":        map[string]any{"source": nil, "bench": "ffmpeg:source#video=mjpeg"},
		"camera_metrics": map[string]bool{"enabled": true},
	}
	if os.Getenv("GO2RTC_RTSP_FAST_PROBE") == "1" {
		// Experiment only: the stock FFmpeg input template and production
		// configuration remain unchanged. Short probing may fail with
		// longer GOPs, extra audio tracks or real camera jitter.
		profile = "bounded_probe_500ms_32k"
		configValues["ffmpeg"] = map[string]string{
			"rtsp": "-fflags nobuffer -flags low_delay -timeout {timeout} -user_agent go2rtc/ffmpeg -rtsp_flags prefer_tcp -analyzeduration 500000 -probesize 32768 -i {input}",
		}
	}
	config, err := json.Marshal(configValues)
	if err != nil {
		ln.Close()
		t.Fatal(err)
	}
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	server := exec.CommandContext(ctx, binary, "-c", string(config))
	server.Dir = dir
	server.Stdout, server.Stderr = io.Discard, io.Discard
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	serverWait := make(chan error, 1)
	go func() { serverWait <- server.Wait() }()
	serverStopped := false
	defer func() {
		if !serverStopped {
			_ = server.Process.Kill()
			<-serverWait
		}
	}()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 40 * time.Second}
	type counts struct {
		Slots     int `json:"producer_slots"`
		Consumers int `json:"consumers"`
		Pending   int `json:"pending_subscriptions"`
	}
	readCounts := func() (map[string]counts, error) {
		pollCtx, stop := context.WithTimeout(ctx, time.Second)
		defer stop()
		req, err := http.NewRequestWithContext(pollCtx, http.MethodGet, "http://bench/api/cameras/metrics", nil)
		if err != nil {
			return nil, err
		}
		res, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			return nil, fmt.Errorf("metrics HTTP %d", res.StatusCode)
		}
		var snapshot struct {
			Cameras []struct {
				ID      string `json:"id"`
				Runtime counts `json:"runtime"`
			} `json:"cameras"`
		}
		if err := json.NewDecoder(io.LimitReader(res.Body, 65536)).Decode(&snapshot); err != nil {
			return nil, err
		}
		m := map[string]counts{}
		for _, camera := range snapshot.Cameras {
			m[camera.ID] = camera.Runtime
		}
		if len(m) != 2 {
			return nil, fmt.Errorf("unexpected isolated registry")
		}
		if _, ok := m["source"]; !ok {
			return nil, fmt.Errorf("source missing")
		}
		if _, ok := m["bench"]; !ok {
			return nil, fmt.Errorf("bench missing")
		}
		return m, nil
	}
	poll := func(want func(map[string]counts) bool) error {
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			m, err := readCounts()
			if err == nil && want(m) {
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
		return fmt.Errorf("expected stream state not observed")
	}
	if err := poll(func(m map[string]counts) bool { return m["source"].Slots == 0 && m["bench"].Slots == 1 }); err != nil {
		t.Fatal(err)
	}
	// The server's own RTSP module accepts the publisher via ANNOUNCE.
	startPublisher := func(fixture string) (*exec.Cmd, chan error, error) {
		cmd := exec.CommandContext(ctx, ffmpeg, "-nostdin", "-hide_banner", "-loglevel", "error", "-re", "-f", "lavfi", "-i", fixture, "-c:v", "libx264", "-threads", "1", "-preset", "ultrafast", "-tune", "zerolatency", "-g", "8", "-pix_fmt", "yuv420p", "-an", "-rtsp_transport", "tcp", "-f", "rtsp", "rtsp://"+address+"/source")
		cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
		if err := cmd.Start(); err != nil {
			return nil, nil, err
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		return cmd, done, nil
	}
	publisher, pubWait, err := startPublisher("testsrc2=size=160x90:rate=8")
	if err != nil {
		t.Fatal(err)
	}
	pubStopped := false
	defer func() {
		if !pubStopped {
			_ = publisher.Process.Kill()
			<-pubWait
		}
	}()
	if err := poll(func(m map[string]counts) bool { return m["source"].Slots == 1 }); err != nil {
		t.Fatal(err)
	}
	before, err := usage()
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan rtspClientSample, 2)
	for i := 0; i < 2; i++ {
		go func() { results <- measureRTSPClient(ctx, client, 80) }()
	}
	var samples []rtspClientSample
	var maxChildren, maxSourceConsumers, maxClients int
	var peak ffmpegProcSample
	seen := map[int]bool{}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for len(samples) < 2 {
		select {
		case r := <-results:
			if r.err != nil {
				t.Fatal(r.err)
			}
			samples = append(samples, r)
		case <-ticker.C:
			children := transcodeChildren(server.Process.Pid)
			if len(children) > maxChildren {
				maxChildren = len(children)
			}
			for pid, s := range children {
				seen[pid] = true
				if s.CPUUserTicks > peak.CPUUserTicks {
					peak.CPUUserTicks = s.CPUUserTicks
				}
				if s.CPUSystemTicks > peak.CPUSystemTicks {
					peak.CPUSystemTicks = s.CPUSystemTicks
				}
				if s.PeakRSSKiB > peak.PeakRSSKiB {
					peak.PeakRSSKiB = s.PeakRSSKiB
				}
			}
			if m, err := readCounts(); err == nil {
				if m["source"].Consumers > maxSourceConsumers {
					maxSourceConsumers = m["source"].Consumers
				}
				if m["bench"].Consumers > maxClients {
					maxClients = m["bench"].Consumers
				}
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	after, err := usage()
	if err != nil {
		t.Fatal(err)
	}
	if maxChildren != 1 || len(seen) != 1 || maxSourceConsumers != 1 || maxClients != 2 || peak.PeakRSSKiB <= 0 || peak.CPUUserTicks+peak.CPUSystemTicks <= 0 {
		t.Fatalf("shared transcode not verified: max children=%d distinct=%d upstream=%d clients=%d sample=%+v", maxChildren, len(seen), maxSourceConsumers, maxClients, peak)
	}
	if err := poll(func(m map[string]counts) bool {
		return m["source"].Consumers == 0 && m["bench"].Consumers == 0 && m["bench"].Pending == 0 && len(transcodeChildren(server.Process.Pid)) == 0
	}); err != nil {
		t.Fatal(err)
	}
	// Reconnect after complete consumer and transcoder cleanup.
	reconnect := measureRTSPClient(ctx, client, 16)
	if reconnect.err != nil {
		t.Fatal(reconnect.err)
	}
	if err := poll(func(m map[string]counts) bool {
		return m["source"].Consumers == 0 && m["bench"].Consumers == 0 && m["bench"].Pending == 0 && len(transcodeChildren(server.Process.Pid)) == 0
	}); err != nil {
		t.Fatal(err)
	}
	var recovery map[string]any
	if outage {
		// Interrupt an active RTSP publisher, not merely an HTTP subscriber.
		// The replacement source is solid red, so decoded red JPEG frames
		// prove that the recovered connection is not replaying stale frames.
		type observation struct {
			at          time.Time
			replacement bool
		}
		isReplacement := func(data []byte) bool {
			img, err := jpeg.Decode(bytes.NewReader(data))
			if err != nil {
				return false
			}
			b := img.Bounds()
			for _, fraction := range []int{1, 2, 3} {
				r, g, bl, _ := img.At(b.Min.X+b.Dx()*fraction/4, b.Min.Y+b.Dy()*fraction/4).RGBA()
				if r < 40000 || g > 12000 || bl > 12000 {
					return false
				}
			}
			return true
		}
		activeCtx, stopActive := context.WithCancel(ctx)
		defer stopActive()
		observations := make(chan observation, 128)
		activeDone := make(chan rtspClientSample, 1)
		go func() {
			activeDone <- measureRTSPClient(activeCtx, client, 96, func(data []byte, at time.Time) {
				observations <- observation{at, isReplacement(data)}
			})
		}()
		// Establish that the original HTTP connection is actually receiving
		// live video before terminating the RTSP source.
		for i := 0; i < 8; i++ {
			select {
			case o := <-observations:
				if o.replacement {
					t.Fatal("original source confused with replacement")
				}
			case out := <-activeDone:
				t.Fatalf("active client stopped before source fault: %v", out.err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
		if err := publisher.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		faultAt := time.Now()
		<-pubWait
		pubStopped = true
		if err := poll(func(m map[string]counts) bool { return m["source"].Slots == 0 }); err != nil {
			t.Fatal(err)
		}
		select {
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		restartedAt := time.Now()
		publisher, pubWait, err = startPublisher("color=c=red:size=160x90:rate=8")
		if err != nil {
			t.Fatal(err)
		}
		pubStopped = false
		if err := poll(func(m map[string]counts) bool { return m["source"].Slots == 1 }); err != nil {
			t.Fatal(err)
		}
		// Report whether the original connection recovered. A fresh request
		// is mandatory even when it did. A timeout is not treated as success
		// of transparent recovery; it is an explicit observed outcome.
		deadline := time.NewTimer(15 * time.Second)
		defer deadline.Stop()
		var redFrames int
		var firstRed time.Time
		activeFinished := false
		sameOutcome := "deadline_without_recovery"
	observe:
		for redFrames < 3 {
			select {
			case o := <-observations:
				if o.replacement && !o.at.Before(restartedAt) {
					if redFrames == 0 {
						firstRed = o.at
					}
					redFrames++
				}
			case <-activeDone:
				activeFinished = true
				sameOutcome = "connection_closed"
				break observe
			case <-deadline.C:
				break observe
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
		if redFrames >= 3 {
			sameOutcome = "recovered"
		}
		stopActive()
		if !activeFinished {
			select {
			case <-activeDone:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
		if err := poll(func(m map[string]counts) bool {
			return m["source"].Consumers == 0 && m["bench"].Consumers == 0 && m["bench"].Pending == 0 && len(transcodeChildren(server.Process.Pid)) == 0
		}); err != nil {
			t.Fatal("outage client cleanup: ", err)
		}
		var freshRed int
		fresh := measureRTSPClient(ctx, client, 16, func(data []byte, _ time.Time) {
			if isReplacement(data) {
				freshRed++
			}
		})
		if fresh.err != nil || freshRed != 16 {
			t.Fatalf("fresh client did not recover replacement source: red=%d error=%v", freshRed, fresh.err)
		}
		if err := poll(func(m map[string]counts) bool {
			return m["source"].Consumers == 0 && m["bench"].Consumers == 0 && m["bench"].Pending == 0 && len(transcodeChildren(server.Process.Pid)) == 0
		}); err != nil {
			t.Fatal("fresh client cleanup: ", err)
		}
		recovery = map[string]any{"source_interrupted": true, "source_republished": true, "observed_outage_ms": millisecond(restartedAt.Sub(faultAt)), "same_connection": sameOutcome, "same_connection_replacement_frames": redFrames, "fresh_client": fresh, "fresh_client_replacement_frames": freshRed, "remaining_consumers": 0, "remaining_transcoders": 0}
		if !firstRed.IsZero() {
			recovery["restart_to_first_replacement_frame_ms"] = millisecond(firstRed.Sub(restartedAt))
		}
	}
	if err := publisher.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	<-pubWait
	pubStopped = true
	if err := poll(func(m map[string]counts) bool { return m["source"].Slots == 0 }); err != nil {
		t.Fatal(err)
	}
	// Read server-only VmHWM before shutdown; wait4 rusage may include
	// reaped child-process usage and must not be labeled server-only RSS.
	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", server.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	var serverRSS int64
	for _, line := range strings.Split(string(status), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "VmHWM:" {
			serverRSS, _ = strconv.ParseInt(fields[1], 10, 64)
		}
	}
	if serverRSS <= 0 {
		t.Fatal("missing server-only VmHWM")
	}
	if err := server.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	err = <-serverWait
	serverStopped = true
	if err != nil {
		t.Fatal(err)
	}
	serverUsage, ok := server.ProcessState.SysUsage().(*syscall.Rusage)
	if !ok {
		t.Fatal("missing server usage")
	}
	pubUsage, ok := publisher.ProcessState.SysUsage().(*syscall.Rusage)
	if !ok {
		t.Fatal("missing publisher usage")
	}
	report := map[string]any{
		"version": "s2-go2rtc-rtsp-v2", "sampled_at": time.Now().UTC().Format(time.RFC3339), "input_profile": profile,
		"physical_camera": false, "rtsp_transcode": true, "source": "synthetic H264 160x90 8fps RTSP over loopback",
		"clients": samples, "reconnect_client": reconnect, "shared_transcoder_peak": maxChildren, "source_consumer_peak": maxSourceConsumers, "concurrent_clients_peak": maxClients, "remaining_consumers": 0, "remaining_transcoders": 0,
		"transcoder_cpu_user_ms_sampled": float64(peak.CPUUserTicks) * 1000 / float64(ticks), "transcoder_cpu_system_ms_sampled": float64(peak.CPUSystemTicks) * 1000 / float64(ticks), "transcoder_peak_rss_kib_sampled": peak.PeakRSSKiB,
		"publisher_cpu_user_ms": timevalMS(pubUsage.Utime), "publisher_cpu_system_ms": timevalMS(pubUsage.Stime), "publisher_peak_rss_kib": pubUsage.Maxrss,
		"go2rtc_wait_rusage_user_ms": millisecond(server.ProcessState.UserTime()), "go2rtc_wait_rusage_system_ms": millisecond(server.ProcessState.SystemTime()), "go2rtc_wait_rusage_peak_rss_kib": serverUsage.Maxrss, "go2rtc_process_peak_rss_kib": serverRSS,
		"client_process_cpu_user_ms": timevalMS(after.Utime) - timevalMS(before.Utime), "client_process_cpu_system_ms": timevalMS(after.Stime) - timevalMS(before.Stime), "client_process_peak_rss_kib": after.Maxrss,
		"resource_scope":       "transcoder sampled lower bounds; publisher/server process lifetime; clients aggregate including metrics polling",
		"queue_depth_measured": false, "capture_to_display_latency_measured": false,
	}
	if recovery != nil {
		report["rtsp_source_recovery"] = recovery
		// Publisher wait usage above covers only the final replacement in
		// this scenario, not the terminated original publisher.
		report["publisher_usage_scope"] = "replacement_process_only"
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(data))
	if output := os.Getenv("GO2RTC_RTSP_REPORT"); output != "" {
		if err := os.WriteFile(output, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
