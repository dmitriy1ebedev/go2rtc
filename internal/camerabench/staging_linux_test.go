//go:build linux

package camerabench

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

type stagingGuideConfig struct {
	App     struct{ Modules []string } `yaml:"app"`
	API     struct{ Listen string }    `yaml:"api"`
	RTSP    struct{ Listen string }    `yaml:"rtsp"`
	Streams map[string]*string         `yaml:"streams"`
	WebOS   struct {
		Enabled       bool
		RefreshMS     int  `yaml:"refresh_ms"`
		CatalogPollMS int  `yaml:"catalog_poll_ms"`
		SoundEnabled  bool `yaml:"sound_enabled"`
		SoundNative   bool `yaml:"sound_native"`
	} `yaml:"webos_pip"`
	Metrics struct{ Enabled bool } `yaml:"camera_metrics"`
}

// Test the actual documented configuration, rather than a duplicate fixture
// that could pass after the installation instructions drift.
func stagingGuide(t *testing.T) ([]byte, stagingGuideConfig) {
	t.Helper()
	raw, err := os.ReadFile("../../docs/server/S4_03_COMPATIBILITY_AND_STAGING.md")
	if err != nil {
		t.Fatal(err)
	}
	marker := []byte("```yaml\n")
	if bytes.Count(raw, marker) != 1 {
		t.Fatal("expected one staging YAML example")
	}
	raw = raw[bytes.Index(raw, marker)+len(marker):]
	end := bytes.Index(raw, []byte("\n```"))
	if end < 0 {
		t.Fatal("unterminated staging example")
	}
	raw = raw[:end]
	var c stagingGuideConfig
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err = decoder.Decode(&c); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.App.Modules, []string{"api", "rtsp", "exec", "ffmpeg", "mjpeg", "webos_pip", "camera_metrics"}) ||
		c.API.Listen != "127.0.0.1:11984" || c.RTSP.Listen != "127.0.0.1:18554" ||
		len(c.Streams) != 2 || c.Streams["demo"] != nil || c.Streams["demo_pip_mjpeg"] == nil ||
		*c.Streams["demo_pip_mjpeg"] != "ffmpeg:demo#video=mjpeg" ||
		!c.WebOS.Enabled || c.WebOS.SoundEnabled || c.WebOS.SoundNative || !c.Metrics.Enabled ||
		c.WebOS.RefreshMS != 240 || c.WebOS.CatalogPollMS != 15000 {
		t.Fatal("staging guide safety or module contract changed")
	}
	if _, ok := c.Streams["demo"]; !ok {
		t.Fatal("missing publisher slot")
	}
	return raw, c
}

func TestStagingGuideConfig(t *testing.T) { stagingGuide(t) }

// The CI verify job launches the checksum-verified downloaded amd64
// artifact, not a locally rebuilt substitute. No camera is contacted.
func TestGo2RTCStagingGuide(t *testing.T) {
	binary := os.Getenv("GO2RTC_BENCH_BINARY")
	if binary == "" {
		t.Skip("requires verified build artifact")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	raw, c := stagingGuide(t)
	for _, addr := range []string{c.API.Listen, c.RTSP.Listen} {
		l, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatalf("isolated port unavailable: %v", err)
		}
		if err = l.Close(); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	cfg := filepath.Join(dir, "go2rtc.yaml")
	if err = os.WriteFile(cfg, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-c", cfg)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	stopped := false
	defer func() {
		if !stopped {
			_ = cmd.Process.Kill()
			<-done
		}
	}()
	tr := &http.Transport{}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: time.Second}
	get := func(path string, out any) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+c.API.Listen+path, nil)
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
	var caps struct {
		Version string
		Cameras []struct {
			ID, Health string
			Streams    struct{ Video, MJPEG string }
			Features   struct {
				PTZ   bool
				Sound bool `json:"sound_activity"`
			}
		}
	}
	for until := time.Now().Add(8 * time.Second); ; {
		if err = get("/api/cameras/capabilities", &caps); err == nil {
			break
		}
		if time.Now().After(until) {
			t.Fatalf("staging API unavailable: %v", err)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	if caps.Version != "v1" || len(caps.Cameras) != 1 {
		t.Fatal("unexpected capability catalog")
	}
	camera := caps.Cameras[0]
	if camera.ID != "demo" || camera.Health != "unknown" || camera.Streams.Video != "demo" || camera.Streams.MJPEG != "demo_pip_mjpeg" || camera.Features.PTZ || camera.Features.Sound {
		t.Fatal("staging capability contract mismatch")
	}
	var catalog struct {
		PTZ     bool `json:"ptz_enabled"`
		Sound   bool `json:"sound_enabled"`
		Cameras []struct {
			Stream string
			MJPEG  string `json:"mjpeg_stream"`
		}
	}
	if err = get("/api/webos-pip", &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.PTZ || catalog.Sound || len(catalog.Cameras) != 1 || catalog.Cameras[0].Stream != "demo" || catalog.Cameras[0].MJPEG != "demo_pip_mjpeg" {
		t.Fatal("legacy catalog mismatch")
	}
	var metrics struct {
		Version string
		Cameras []struct {
			ID      string
			Runtime struct {
				Slots     int `json:"producer_slots"`
				Consumers int
				Pending   int `json:"pending_subscriptions"`
			}
		}
	}
	if err = get("/api/cameras/metrics", &metrics); err != nil {
		t.Fatal(err)
	}
	if metrics.Version != "v1" || len(metrics.Cameras) != 1 || metrics.Cameras[0].ID != "demo" ||
		metrics.Cameras[0].Runtime.Slots != 0 || metrics.Cameras[0].Runtime.Consumers != 0 || metrics.Cameras[0].Runtime.Pending != 0 {
		t.Fatal("unexpected source or subscription")
	}
	if len(transcodeChildren(cmd.Process.Pid)) != 0 {
		t.Fatal("idle staging started FFmpeg")
	}
	if err = cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	err = <-done
	stopped = true
	if err != nil {
		t.Fatal(err)
	}
	t.Log("verified downloaded binary, exact documented YAML, both catalog contracts, idle runtime metrics and graceful isolated shutdown")
}
