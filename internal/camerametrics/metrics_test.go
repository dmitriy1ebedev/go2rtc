package camerametrics

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AlexxIT/go2rtc/internal/streams"
)

func TestMetricsAPIReadOnlySortedAndNoSecrets(t *testing.T) {
	names := []string{"z", "z_pip_mjpeg", "z_tv_aac", "a", "a_pip_max_mjpeg", "a", "rtsp://name:secret@example.invalid"}
	get := func() []string { return append([]string(nil), names...) }
	counts := map[string]streams.RuntimeCounts{
		"a": {ProducerSlots: 1, Consumers: 2, PendingSubscriptions: 0},
		"z": {ProducerSlots: 3, Consumers: 1, PendingSubscriptions: 2},
	}
	read := func(name string) (streams.RuntimeCounts, bool) {
		v, ok := counts[name]
		return v, ok
	}
	serve := handler(get, read)
	rec := httptest.NewRecorder()
	serve(rec, httptest.NewRequest(http.MethodGet, "/api/cameras/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" ||
		rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing read-only response headers")
	}
	var result Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Version != "v1" || len(result.Cameras) != 2 ||
		result.Cameras[0].ID != "a" || result.Cameras[1].ID != "z" ||
		result.Cameras[0].Runtime.Consumers != 2 ||
		result.Cameras[1].Runtime.PendingSubscriptions != 2 {
		t.Fatalf("incorrect camera metric snapshot: %+v", result)
	}
	if _, err := time.Parse(time.RFC3339Nano, result.SampledAt); err != nil {
		t.Fatalf("invalid sample time: %q (%v)", result.SampledAt, err)
	}
	body := rec.Body.String()
	for _, secret := range []string{"secret", "rtsp://", "example.invalid", "fps", "bitrate", "first_frame", "ffmpeg", "url"} {
		if strings.Contains(body, secret) {
			t.Errorf("unsafe or unmeasured value %q in snapshot: %s", secret, body)
		}
	}
}

func TestMetricsAPIOriginMethodAndLiveStreamChanges(t *testing.T) {
	names := []string{"entrance"}
	serve := handler(func() []string { return names }, func(name string) (streams.RuntimeCounts, bool) {
		return streams.RuntimeCounts{ProducerSlots: 1}, name == "entrance"
	})
	for _, tc := range []struct {
		name, method, origin string
		want                 int
	}{
		{"file origin", "GET", "file://", http.StatusForbidden},
		{"null origin", "GET", "null", http.StatusForbidden},
		{"foreign browser", "GET", "https://untrusted.invalid", http.StatusForbidden},
		{"post", "POST", "", http.StatusMethodNotAllowed},
		{"put", "PUT", "", http.StatusMethodNotAllowed},
		{"same origin", "GET", "", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/api/cameras/metrics", nil)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			rec := httptest.NewRecorder()
			serve(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status %d, expected %d", rec.Code, tc.want)
			}
			if rec.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Fatal("metrics endpoint must not allow cross-origin reads")
			}
		})
	}
	names = nil
	rec := httptest.NewRecorder()
	serve(rec, httptest.NewRequest(http.MethodGet, "/api/cameras/metrics", nil))
	var result Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Cameras) != 0 || result.Cameras == nil {
		t.Fatalf("removed streams must not remain cached: %+v", result)
	}
}

// The upstream api.origin="*" middleware runs before registered API
// handlers. The metrics handler must remove its wildcard headers even
// when a request has no Origin header.
func TestMetricsClearsGlobalWildcardCORS(t *testing.T) {
	endpoint := handler(func() []string { return []string{"front"} },
		func(string) (streams.RuntimeCounts, bool) {
			return streams.RuntimeCounts{Consumers: 1}, true
		})
	globalCORS := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Access-Control-Expose-Headers", "X-Test")
		endpoint.ServeHTTP(w, r)
	})
	for _, tc := range []struct {
		name, origin string
		want         int
	}{
		{"without origin", "", http.StatusOK},
		{"cross origin", "https://untrusted.invalid", http.StatusForbidden},
		{"file origin", "null", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/cameras/metrics", nil)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			rec := httptest.NewRecorder()
			globalCORS.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status=%d, want=%d", rec.Code, tc.want)
			}
			for _, key := range []string{
				"Access-Control-Allow-Origin", "Access-Control-Allow-Methods",
				"Access-Control-Allow-Headers", "Access-Control-Expose-Headers",
			} {
				if got := rec.Header().Get(key); got != "" {
					t.Errorf("inherited CORS header %s=%q", key, got)
				}
			}
		})
	}
}

func TestMetricsAPICapsPhysicalStreamsWithoutCallingUnknown(t *testing.T) {
	names := make([]string, 0, 85)
	for i := 0; i < 80; i++ {
		names = append(names, fmt.Sprintf("cam%02d", i))
	}
	for i := 0; i < 5; i++ {
		names = append(names, fmt.Sprintf("cam%02d_pip_mjpeg", i))
	}
	calls := 0
	serve := handler(func() []string { return names }, func(name string) (streams.RuntimeCounts, bool) {
		calls++
		return streams.RuntimeCounts{Consumers: 1}, true
	})
	rec := httptest.NewRecorder()
	serve(rec, httptest.NewRequest(http.MethodGet, "/api/cameras/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var result Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Cameras) != 64 || calls != 64 {
		t.Fatalf("unbounded metrics enumeration: cameras=%d readers=%d", len(result.Cameras), calls)
	}
}
