//go:build linux

package camerabench

import (
	"context"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestGo2RTCRTSPBlackholeAcceptance is an opt-in integration scenario:
// go test -run '^TestGo2RTCRTSPBlackholeAcceptance$' ./internal/camerabench
// The ordinary offline unit suite does not build or launch a server.
// A deliberately selected acceptance run must execute the entire fixture;
// a missing compiler, FFmpeg or report is a failure, not a skipped test.
func TestGo2RTCRTSPBlackholeAcceptance(t *testing.T) {
	selected := flag.Lookup("test.run")
	if selected == nil || selected.Value.String() != "^TestGo2RTCRTSPBlackholeAcceptance$" {
		t.Skip("select this isolated integration scenario explicitly")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Fatal("FFmpeg fixture dependency: ", err)
	}
	tmp := t.TempDir()
	binary := filepath.Join(tmp, "go2rtc")
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-p=1", "-trimpath", "-buildvcs=false", "-o", binary, ".")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("isolated fixture build: %v\n%s", err, output)
	}
	report := filepath.Join(tmp, "blackhole.json")
	// Keep this scenario independent of optional benchmark modes in the
	// caller environment. The same real RTSP fixture is exercised in CI.
	for _, key := range []string{
		"GO2RTC_RTSP_OUTAGE", "GO2RTC_RTSP_SOAK",
		"GO2RTC_RTSP_LONG_GOP_AAC", "GO2RTC_RTSP_SNAPSHOT_LOAD",
		"GO2RTC_RTSP_FAST_PROBE", "GO2RTC_RTSP_CONSERVATIVE_PROBE",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("GO2RTC_BENCH_BINARY", binary)
	t.Setenv("GO2RTC_RTSP_BLACKHOLE", "1")
	t.Setenv("GO2RTC_RTSP_REPORT", report)
	TestGo2RTCRTSPBaseline(t)
	data, err := os.ReadFile(report)
	if err != nil {
		t.Fatal("acceptance report missing: ", err)
	}
	// Retain a unique report only after every fixture assertion passed.
	// This is an ignored local test artifact, never an installed binary.
	dir := filepath.Join(root, ".tmp")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.CreateTemp(dir, "s2-blackhole-*.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	t.Logf("verified recovery report: %s", f.Name())
}
