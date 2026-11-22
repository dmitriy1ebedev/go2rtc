package webospip

import (
	"context"
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
