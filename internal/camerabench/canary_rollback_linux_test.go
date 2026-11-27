//go:build linux

package camerabench

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

const canaryArtifactLimit = 128 << 20

type canaryInput struct {
	Baseline       string `json:"baseline"`
	BaselineSHA256 string `json:"baseline_sha256"`
	BaselineCommit string `json:"baseline_commit"`
	SoakSeconds    int    `json:"soak_seconds"`
	Fault          string `json:"fault,omitempty"`
}

func validCanaryDigest(s string, size int) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == size && s == strings.ToLower(s)
}

func canaryDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() <= 0 || st.Size() > canaryArtifactLimit {
		return "", errors.New("invalid canary artifact")
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, canaryArtifactLimit+1))
	if err != nil || n != st.Size() {
		return "", errors.New("incomplete canary artifact read")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// The active path belongs exclusively to a private fixture directory.
// A wrong digest must leave the previous executable completely intact.
func stageCanaryBinary(source, expected, active string) error {
	if !validCanaryDigest(expected, 32) {
		return errors.New("invalid expected artifact digest")
	}
	f, err := os.Open(source)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() <= 0 || st.Size() > canaryArtifactLimit {
		return errors.New("invalid source artifact")
	}
	tmp, err := os.CreateTemp(filepath.Dir(active), ".candidate-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(f, canaryArtifactLimit+1))
	if err != nil || n != st.Size() || hex.EncodeToString(h.Sum(nil)) != expected {
		return errors.New("candidate artifact digest mismatch")
	}
	if err = tmp.Chmod(0700); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), active)
}

func TestCanaryStageRejectsWrongChecksum(t *testing.T) {
	dir := t.TempDir()
	source, active := filepath.Join(dir, "source"), filepath.Join(dir, "active")
	if err := os.WriteFile(source, []byte("verified original"), 0600); err != nil {
		t.Fatal(err)
	}
	digest, err := canaryDigest(source)
	if err != nil {
		t.Fatal(err)
	}
	if err = stageCanaryBinary(source, digest, active); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(source, []byte("corrupt replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = stageCanaryBinary(source, digest, active); err == nil {
		t.Fatal("wrong checksum accepted")
	}
	if got, err := canaryDigest(active); err != nil || got != digest {
		t.Fatalf("previous artifact changed: digest=%s err=%v", got, err)
	}
}

type canaryProcess struct {
	cmd       *exec.Cmd
	done      chan error
	transport *http.Transport
	client    *http.Client
	stopped   bool
}

func (p *canaryProcess) halt(crash bool) error {
	if p.stopped {
		return nil
	}
	p.stopped = true
	p.transport.CloseIdleConnections()
	var signalErr error
	if crash {
		signalErr = p.cmd.Process.Kill()
	} else {
		signalErr = p.cmd.Process.Signal(os.Interrupt)
	}
	if signalErr != nil {
		_ = p.cmd.Process.Kill()
		err := <-p.done
		if crash && errors.Is(signalErr, os.ErrProcessDone) && err != nil {
			return nil
		}
		return signalErr
	}
	select {
	case err := <-p.done:
		if crash && err != nil {
			return nil
		}
		if crash {
			return errors.New("expected isolated candidate crash")
		}
		return err
	case <-time.After(5 * time.Second):
		_ = p.cmd.Process.Kill()
		<-p.done
		return errors.New("isolated process failed to exit in time")
	}
}

func (p *canaryProcess) counts(ctx context.Context) (int, int, error) {
	var out struct {
		Version string
		Cameras []struct {
			ID      string
			Runtime struct {
				Consumers int `json:"consumers"`
				Pending   int `json:"pending_subscriptions"`
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://bench/api/cameras/metrics", nil)
	if err != nil {
		return 0, 0, err
	}
	res, err := p.client.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return 0, 0, fmt.Errorf("metrics HTTP %d", res.StatusCode)
	}
	if err = json.NewDecoder(io.LimitReader(res.Body, 65536)).Decode(&out); err != nil {
		return 0, 0, err
	}
	if out.Version != "v1" || len(out.Cameras) != 1 || out.Cameras[0].ID != "bench" {
		return 0, 0, errors.New("unexpected isolated registry")
	}
	return out.Cameras[0].Runtime.Consumers, out.Cameras[0].Runtime.Pending, nil
}

func (p *canaryProcess) idle(ctx context.Context) error {
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		c, pending, err := p.counts(ctx)
		if err == nil && c == 0 && pending == 0 && len(transcodeChildren(p.cmd.Process.Pid)) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	return errors.New("isolated idle state not observed")
}

func startCanary(t *testing.T, ctx context.Context, dir, active, source string) (*canaryProcess, error) {
	t.Helper()
	socket := filepath.Join(dir, "api.sock")
	// Caller has already reaped any previous child owning this private socket.
	if err := os.Remove(socket); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	config, err := json.Marshal(map[string]any{
		"app":            map[string]any{"modules": []string{"api", "http", "mjpeg", "camera_metrics"}},
		"api":            map[string]any{"listen": "", "unix_listen": socket},
		"streams":        map[string]string{"bench": source},
		"camera_metrics": map[string]bool{"enabled": true},
	})
	if err != nil {
		return nil, err
	}
	cfg := filepath.Join(dir, "fixture.json")
	if err = os.WriteFile(cfg, config, 0600); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, active, "-c", cfg)
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, io.Discard, io.Discard
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	p := &canaryProcess{cmd: cmd, done: make(chan error, 1), transport: tr, client: &http.Client{Transport: tr}}
	go func() { p.done <- cmd.Wait() }()
	t.Cleanup(func() {
		if !p.stopped {
			_ = p.halt(true)
		}
	})
	if err = p.idle(ctx); err != nil {
		return p, err
	}
	return p, nil
}

func requireCanaryStart(t *testing.T, ctx context.Context, dir, active, source string) *canaryProcess {
	t.Helper()
	p, err := startCanary(t, ctx, dir, active, source)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

type canarySource struct {
	server   *httptest.Server
	mu       sync.Mutex
	sent     [64]time.Time
	requests atomic.Int64
	active   atomic.Int64
	paused   atomic.Bool
}

func canaryJPEGSource(t *testing.T) *canarySource {
	t.Helper()
	frames := make([][]byte, 64)
	for seq := 1; seq <= len(frames); seq++ {
		rgb := markerRGB(seq)
		img := image.NewRGBA(image.Rect(0, 0, markerWidth, markerHeight))
		for i := 0; i < len(rgb)/3; i++ {
			copy(img.Pix[i*4:i*4+3], rgb[i*3:i*3+3])
			img.Pix[i*4+3] = 255
		}
		var b bytes.Buffer
		if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 60}); err != nil {
			t.Fatal(err)
		}
		frames[seq-1] = b.Bytes()
	}
	source := &canarySource{}
	source.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		source.requests.Add(1)
		source.active.Add(1)
		defer source.active.Add(-1)
		w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=fixture")
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for i := 0; ; {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
			}
			if source.paused.Load() {
				continue
			}
			idx := i % len(frames)
			i++
			frame := frames[idx]
			source.mu.Lock()
			source.sent[idx] = time.Now()
			source.mu.Unlock()
			if _, err := fmt.Fprintf(w, "--fixture\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", len(frame)); err != nil {
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
	return source
}

type canaryPlayback struct {
	rtspClientSample
	MaxFrameGapMS  float64
	MeanLatencyMS  float64
	MaxLatencyMS   float64
	HealthSamples  int
	SourceRequests int64
}

func checkCanaryFrames(ctx context.Context, p *canaryProcess, source *canarySource, n int, injectStall bool) (out canaryPlayback, err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// A stalled read must not consume the entire requested soak duration.
	// Use a monotonic elapsed time and a single watchdog goroutine.
	started := time.Now()
	var lastProgress atomic.Int64
	var stalled atomic.Bool
	watchDone := make(chan struct{})
	watchExit := make(chan struct{})
	go func() {
		defer close(watchExit)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-watchDone:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if time.Since(started)-time.Duration(lastProgress.Load()) > 5*time.Second {
					stalled.Store(true)
					cancel()
					return
				}
			}
		}
	}()
	before := source.requests.Load()
	var last int
	var lastAt, lastHealth time.Time
	var totalLatency float64
	var observedErr error
	out.rtspClientSample = measureRTSPClient(ctx, p.client, n, func(data []byte, at time.Time) {
		if observedErr != nil {
			return
		}
		fail := func(err error) {
			observedErr = err
			cancel()
		}
		img, err := jpeg.Decode(bytes.NewReader(data))
		if err != nil {
			fail(err)
			return
		}
		seq, err := markerSequence(img)
		if err != nil || seq < 1 || seq > len(source.sent) || (last != 0 && seq != last%len(source.sent)+1) {
			fail(fmt.Errorf("invalid or discontinuous source marker %d after %d", seq, last))
			return
		}
		source.mu.Lock()
		sent := source.sent[seq-1]
		source.mu.Unlock()
		if sent.IsZero() || sent.After(at) {
			fail(errors.New("invalid source timestamp"))
			return
		}
		latency := millisecond(time.Since(sent))
		if latency > 1500 {
			fail(fmt.Errorf("source-to-JPEG latency %.1fms exceeds 1500ms", latency))
			return
		}
		totalLatency += latency
		if latency > out.MaxLatencyMS {
			out.MaxLatencyMS = latency
		}
		if !lastAt.IsZero() {
			gap := millisecond(at.Sub(lastAt))
			if gap > out.MaxFrameGapMS {
				out.MaxFrameGapMS = gap
			}
			if gap > 1500 {
				fail(fmt.Errorf("frame gap %.1fms exceeds 1500ms", gap))
				return
			}
		}
		last, lastAt = seq, at
		lastProgress.Store(int64(time.Since(started)))
		if injectStall && seq == 8 {
			source.paused.Store(true)
		}
		if lastHealth.IsZero() || at.Sub(lastHealth) >= 5*time.Second {
			c, pending, err := p.counts(ctx)
			if err != nil || c != 1 || pending != 0 || len(transcodeChildren(p.cmd.Process.Pid)) != 0 {
				fail(fmt.Errorf("active health gate: consumers=%d pending=%d err=%v", c, pending, err))
				return
			}
			out.HealthSamples++
			lastHealth = at
		}
	})
	close(watchDone)
	<-watchExit
	out.SourceRequests = source.requests.Load() - before
	if stalled.Load() {
		return out, errCanaryFrameStall
	}
	if observedErr != nil {
		return out, observedErr
	}
	if out.err != nil || out.Frames != n || out.FPS < 3 || out.FPS > 5 || out.SourceRequests != 1 {
		return out, fmt.Errorf("invalid isolated playback: frames=%d fps=%.2f sources=%d err=%v", out.Frames, out.FPS, out.SourceRequests, out.err)
	}
	out.MeanLatencyMS = totalLatency / float64(n)
	if err = p.idle(ctx); err != nil {
		return out, err
	}
	deadline := time.Now().Add(5 * time.Second)
	for source.active.Load() != 0 {
		if time.Now().After(deadline) {
			return out, errors.New("source subscriber not released")
		}
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	return out, nil
}

var errCanaryFrameStall = errors.New("canary frame watchdog expired")

func requireCanaryFrames(t *testing.T, ctx context.Context, p *canaryProcess, source *canarySource, n int) canaryPlayback {
	t.Helper()
	out, err := checkCanaryFrames(ctx, p, source, n, false)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func canaryUsage(p *canaryProcess) (map[string]any, error) {
	if p.cmd.ProcessState == nil {
		return nil, errors.New("candidate not reaped")
	}
	r, ok := p.cmd.ProcessState.SysUsage().(*syscall.Rusage)
	if !ok || r.Maxrss <= 0 {
		return nil, errors.New("missing isolated candidate rusage")
	}
	return map[string]any{
		"cpu_user_ms": float64(p.cmd.ProcessState.UserTime()) / float64(time.Millisecond),
		"cpu_system_ms": float64(p.cmd.ProcessState.SystemTime()) / float64(time.Millisecond),
		"peak_rss_kib": r.Maxrss,
	}, nil
}

// Explicit, source-only staging test. The local input manifest contains a
// previously verified test artifact, not a production path or configuration.
// A short soak is evidence for this test only, not a 24-hour deployment soak.
func TestGo2RTCCanaryRollbackAcceptance(t *testing.T) {
	selected := flag.Lookup("test.run")
	if selected == nil || selected.Value.String() != "^TestGo2RTCCanaryRollbackAcceptance$" {
		t.Skip("select isolated rollback acceptance explicitly")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	input, err := os.ReadFile(filepath.Join(root, ".tmp", "s4-canary-input.json"))
	if err != nil || len(input) > 4096 {
		t.Fatal("explicit verified baseline manifest required")
	}
	var spec canaryInput
	d := json.NewDecoder(bytes.NewReader(input))
	d.DisallowUnknownFields()
	if err = d.Decode(&spec); err != nil {
		t.Fatal(err)
	}
	if err = d.Decode(new(any)); err != io.EOF || !filepath.IsAbs(spec.Baseline) ||
		!validCanaryDigest(spec.BaselineSHA256, 32) || !validCanaryDigest(spec.BaselineCommit, 20) ||
		spec.SoakSeconds < 10 || spec.SoakSeconds > 86400 || (spec.Fault != "" && spec.Fault != "frame_stall" && spec.Fault != "startup_failure") {
		t.Fatal("invalid baseline manifest")
	}
	tmp := t.TempDir()
	candidate := filepath.Join(tmp, "candidate")
	buildCtx, stopBuild := context.WithTimeout(context.Background(), 12*time.Minute)
	defer stopBuild()
	build := exec.CommandContext(buildCtx, "go", "build", "-p=1", "-trimpath", "-buildvcs=false", "-o", candidate, ".")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("test-only candidate build: %v\n%s", err, output)
	}
	if spec.Fault == "startup_failure" {
		// A digest can be valid even when a test artifact cannot execute.
		// Use inert bytes in the private fixture, never a production file.
		if err = os.WriteFile(candidate, []byte("not an ELF executable"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	digest, err := canaryDigest(candidate)
	if err != nil || digest == spec.BaselineSHA256 {
		t.Fatalf("distinct candidate artifact required: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(spec.SoakSeconds+90)*time.Second)
	defer cancel()
	source := canaryJPEGSource(t)
	t.Cleanup(source.server.Close) // run after isolated child cleanup, even on failure
	dir := filepath.Join(tmp, "canary")
	if err = os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	active := filepath.Join(dir, "go2rtc")
	if err = stageCanaryBinary(spec.Baseline, spec.BaselineSHA256, active); err != nil {
		t.Fatal(err)
	}
	old := requireCanaryStart(t, ctx, dir, active, source.server.URL)
	before := requireCanaryFrames(t, ctx, old, source, 12)
	if err = old.halt(false); err != nil {
		t.Fatal(err)
	}
	var current *canaryProcess
	var soak canaryPlayback
	healthErr := stageCanaryBinary(candidate, digest, active)
	if healthErr == nil {
		current, healthErr = startCanary(t, ctx, dir, active, source.server.URL)
	}
	startupFailed := healthErr != nil
	start := time.Now()
	if healthErr == nil {
		soakCtx, stopSoak := context.WithTimeout(ctx, time.Duration(spec.SoakSeconds+15)*time.Second)
		soak, healthErr = checkCanaryFrames(soakCtx, current, source, 4*spec.SoakSeconds+1, spec.Fault == "frame_stall")
		stopSoak()
	}
	duration := time.Since(start)
	if healthErr == nil && duration < time.Duration(spec.SoakSeconds-1)*time.Second {
		healthErr = errors.New("candidate soak shorter than requested")
	}
	// A health failure takes the same isolated rollback path as the
	// deliberate crash. Keep the parent context alive for recovery.
	trigger := "injected_candidate_crash"
	if healthErr != nil {
		trigger = "failed_health_gate"
	}
	if startupFailed {
		trigger = "failed_startup_gate"
	}
	var stopErr error
	if current != nil {
		stopErr = current.halt(true)
	}
	source.paused.Store(false)
	for until := time.Now().Add(5 * time.Second); source.active.Load() != 0; {
		if time.Now().After(until) {
			t.Fatal("isolated failed candidate did not release its source")
		}
		time.Sleep(25 * time.Millisecond)
	}
	var usage map[string]any
	if current != nil {
		if _, _, err = current.counts(ctx); err == nil {
			t.Fatal("isolated failed candidate still serves requests")
		}
		usage, err = canaryUsage(current)
		if err != nil {
			stopErr = errors.Join(stopErr, err)
		}
	}
	rollbackStart := time.Now()
	if err = stageCanaryBinary(spec.Baseline, spec.BaselineSHA256, active); err != nil {
		t.Fatal(err)
	}
	rolled := requireCanaryStart(t, ctx, dir, active, source.server.URL)
	after := requireCanaryFrames(t, ctx, rolled, source, 12)
	recovery := time.Since(rollbackStart)
	got, err := canaryDigest(active)
	if err != nil || got != spec.BaselineSHA256 {
		t.Fatal("rollback artifact differs from original")
	}
	if err = rolled.halt(false); err != nil {
		t.Fatal(err)
	}
	report := map[string]any{
		"baseline_commit": spec.BaselineCommit, "baseline_sha256": spec.BaselineSHA256,
		"candidate_sha256": digest, "rollback_sha256": got,
		"baseline_fps": before.FPS, "candidate_fps": soak.FPS, "rollback_fps": after.FPS,
		"candidate_frames": soak.Frames, "candidate_soak_ms": millisecond(duration),
		"candidate_first_frame_ms": soak.FirstFrameMS, "candidate_max_frame_gap_ms": soak.MaxFrameGapMS,
		"source_to_jpeg_mean_ms": soak.MeanLatencyMS, "source_to_jpeg_max_ms": soak.MaxLatencyMS,
		"candidate_health_samples": soak.HealthSamples, "candidate_source_requests": soak.SourceRequests,
		"candidate_process_usage": usage, "rollback_trigger": trigger,
		"requested_fault": spec.Fault, "frame_watchdog_triggered": errors.Is(healthErr, errCanaryFrameStall),
		"candidate_startup_failed": startupFailed,
		"rollback_first_frame_ms": after.FirstFrameMS, "baseline_first_frame_ms": before.FirstFrameMS,
		"rollback_verified_ms": millisecond(recovery), "remaining_consumers": 0,
		"remaining_transcoders": 0, "production_contacted": false,
		"isolated_candidate_crash_verified": current != nil && stopErr == nil, "rollback_playback_verified": true,
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.CreateTemp(filepath.Join(root, ".tmp"), "s4-canary-rollback-*.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	t.Logf("verified isolated rollback report: %s", f.Name())
	if stopErr != nil {
		t.Fatalf("isolated process teardown failed; rollback verified: %v", stopErr)
	}
	if spec.Fault == "startup_failure" {
		if !startupFailed || current != nil {
			t.Fatalf("expected inert executable admission failure, got %v", healthErr)
		}
	} else if spec.Fault == "frame_stall" {
		if !errors.Is(healthErr, errCanaryFrameStall) {
			t.Fatalf("expected a frame-watchdog failure, got %v", healthErr)
		}
	} else if healthErr != nil {
		t.Fatalf("candidate health gate failed; isolated rollback verified: %v", healthErr)
	}
}
