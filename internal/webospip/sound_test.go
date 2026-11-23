package webospip

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSoundRetryDelayBounded(t *testing.T) {
	tests := []struct {
		failures int
		want     time.Duration
	}{
		{0, 3 * time.Second}, {1, 6 * time.Second},
		{2, 12 * time.Second}, {3, 24 * time.Second},
		{4, 48 * time.Second}, {5, 60 * time.Second},
		{80, 60 * time.Second},
	}
	for _, tt := range tests {
		if got := soundRetryDelay(tt.failures); got != tt.want {
			t.Fatalf("failures=%d got=%s want=%s", tt.failures, got, tt.want)
		}
	}
}

func TestSoundMonitorStopsOnCancelledContext(t *testing.T) {
	monitor := NewSoundMonitor(-32, 3*time.Second, "8554")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		monitor.RunContext(ctx, "door")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("monitor ignored cancellation before spawning ffmpeg")
	}
}

func TestSoundActivityStatesWithoutFFmpeg(t *testing.T) {
	m := NewSoundMonitor(-32, 3*time.Second, "8554")
	getState := func() string {
		t.Helper()
		rec := httptest.NewRecorder()
		m.Handler(rec, httptest.NewRequest(http.MethodGet, "/api/webos-pip/activity", nil))
		if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("unexpected sound API response: %d", rec.Code)
		}
		var body struct {
			State string `json:"state"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.State
	}
	if state := getState(); state != "unavailable" {
		t.Fatalf("uninitialized monitor should be unavailable, got %q", state)
	}
	m.observe(-20)
	if state := getState(); state != "active" {
		t.Fatalf("loud sample should activate monitor, got %q", state)
	}
	m.observe(-70)
	if state := getState(); state != "active" {
		t.Fatalf("quiet sample should honor hold window, got %q", state)
	}
	m.mu.Lock()
	m.activeUntil = time.Now().Add(-time.Second)
	m.lastSample = time.Now()
	m.mu.Unlock()
	if state := getState(); state != "quiet" {
		t.Fatalf("expired hold with recent data should be quiet, got %q", state)
	}
	m.mu.Lock()
	m.lastSample = time.Now().Add(-4 * time.Second)
	m.mu.Unlock()
	if state := getState(); state != "unavailable" {
		t.Fatalf("stale samples must be unavailable, got %q", state)
	}
	if rmsDBFS(nil) != -120 || rmsDBFS([]byte{1}) != -120 ||
		rmsDBFS([]byte{0, 0, 0, 0}) != -120 {
		t.Fatal("invalid or silent PCM should yield silence floor")
	}
	halfScale := rmsDBFS([]byte{0, 64, 0, 64})
	if halfScale <= -7 || halfScale >= -5 {
		t.Fatalf("16-bit PCM RMS is not near -6 dBFS: %g", halfScale)
	}
}

func TestCameraAndActivityWebOSOrigins(t *testing.T) {
	monitor := NewSoundMonitor(-32, 3*time.Second, "8554")
	catalog := handler(Config{}, func() []string { return []string{"door"} })
	endpoints := map[string]http.HandlerFunc{
		"catalog":  catalog,
		"activity": monitor.Handler,
	}
	for endpoint, serve := range endpoints {
		for _, tt := range []struct {
			origin string
			status int
			allow  string
		}{
			{"", http.StatusOK, ""},
			{"null", http.StatusOK, "null"},
			{"file://", http.StatusOK, "file://"},
			{"file://io.go2rtc.webos.pip", http.StatusOK, "file://io.go2rtc.webos.pip"},
			{"file://io.go2rtc.webos.pip-webos", http.StatusOK, "file://io.go2rtc.webos.pip-webos"},
			{"https://example.invalid", http.StatusForbidden, ""},
		} {
			req := httptest.NewRequest(http.MethodGet, "/api/webos-pip", nil)
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			rec := httptest.NewRecorder()
			serve(rec, req)
			if rec.Code != tt.status {
				t.Errorf("%s origin=%q got status %d want %d", endpoint, tt.origin, rec.Code, tt.status)
			}
			if allow := rec.Header().Get("Access-Control-Allow-Origin"); allow != tt.allow {
				t.Errorf("%s origin=%q got Allow-Origin %q want %q", endpoint, tt.origin, allow, tt.allow)
			}
		}
	}
}
