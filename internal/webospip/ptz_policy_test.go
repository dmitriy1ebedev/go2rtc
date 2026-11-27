package webospip

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func policyConfig(name, token string) PTZConfig {
	return PTZConfig{Enabled: true, Camera: name, Token: strings.Repeat(token, 48)}
}
func policyRequest(path, token, direction string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"direction":"`+direction+`"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Go2RTC-PTZ", token)
	return r
}

func TestPTZPoliciesIsolateCameras(t *testing.T) {
	a, b := policyConfig("door", "A"), policyConfig("yard", "B")
	old, controllers := buildPTZPolicies(a, []PTZConfig{b}, nil, func(string) bool { return true })
	if old == nil || controllers["door"] != old || len(controllers) != 2 {
		t.Fatal("legacy and universal controllers must share policy")
	}
	var door, yard atomic.Int32
	old.move = func(string) error { door.Add(1); return nil }
	controllers["yard"].move = func(string) error { yard.Add(1); return nil }
	handler := routePTZ(controllers)
	for _, tc := range []struct {
		query, token string
		status       int
	}{
		{"camera=door", b.Token, 401}, {"camera=yard", a.Token, 401},
		{"camera=door&camera=yard", a.Token, 400}, {"camera=door&host=x", a.Token, 400},
		{"camera=unknown", a.Token, 404}, {"camera=door", a.Token, 200}, {"camera=yard", b.Token, 200},
	} {
		w := httptest.NewRecorder()
		handler(w, policyRequest("/api/cameras/ptz?"+tc.query, tc.token, "up"))
		if w.Code != tc.status {
			t.Errorf("query=%s status=%d want=%d", tc.query, w.Code, tc.status)
		}
	}
	if door.Load() != 1 || yard.Load() != 1 {
		t.Fatalf("unexpected motor calls %d %d", door.Load(), yard.Load())
	}
	_, bad := buildPTZPolicies(a, []PTZConfig{b, policyConfig("yard", "C"), policyConfig("other", "A"), policyConfig("hidden_pip_mjpeg", "D")}, nil, func(string) bool { return true })
	if len(bad) != 1 || bad["door"] == nil {
		t.Fatal("duplicate names/tokens or technical streams accepted")
	}
}

func TestPTZRejectsBurstWithoutQueuedMotion(t *testing.T) {
	ctl := newPTZController(policyConfig("door", "A"))
	entered, release := make(chan struct{}), make(chan struct{})
	var moves, stops atomic.Int32
	ctl.move = func(direction string) error {
		if direction == "stop" {
			stops.Add(1)
			return nil
		}
		moves.Add(1)
		close(entered)
		<-release
		return nil
	}
	first := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		ctl.handler(w, policyRequest("/api/webos-pip/ptz", ctl.cfg.Token, "left"))
		first <- w.Code
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("move did not start")
	}
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	for i := 0; i < 24; i++ {
		w := httptest.NewRecorder()
		ctl.handler(w, policyRequest("/api/webos-pip/ptz", ctl.cfg.Token, "right"))
		if w.Code != 429 {
			t.Errorf("burst was queued or accepted: %d", w.Code)
		}
	}
	stop := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		ctl.handler(w, policyRequest("/api/webos-pip/ptz", ctl.cfg.Token, "stop"))
		stop <- w.Code
	}()
	deadline := time.Now().Add(time.Second)
	for !ctl.stopPending.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !ctl.stopPending.Load() {
		t.Fatal("stop waiter not registered")
	}
	w := httptest.NewRecorder()
	ctl.handler(w, policyRequest("/api/webos-pip/ptz", ctl.cfg.Token, "stop"))
	if w.Code != 429 {
		t.Errorf("second stop waiter accepted: %d", w.Code)
	}
	close(release)
	select {
	case got := <-first:
		if got != 200 {
			t.Errorf("first %d", got)
		}
	case <-time.After(time.Second):
		t.Fatal("first request stuck")
	}
	select {
	case got := <-stop:
		if got != 200 {
			t.Errorf("stop %d", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stop request stuck")
	}
	if moves.Load() != 1 || stops.Load() != 1 {
		t.Fatal("stale commands executed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if ctl.lockCommand(ctx, "up") {
		ctl.mu.Unlock()
		t.Fatal("cancelled request admitted")
	}
}

func TestPTZRejectsAmbiguousSources(t *testing.T) {
	lookup := func(string) ([]net.IP, error) { return nil, nil }
	if _, err := resolveONVIFSource([]string{"rtsp://u:p@192.168.1.10/a", "rtsp://u:p@192.168.1.11/b"}, 2020, lookup); err == nil {
		t.Fatal("ambiguous camera accepted")
	}
	if _, err := resolveONVIFSource([]string{"rtsp://u:p@192.168.1.10/a", "rtsp://u:p@192.168.1.10/b"}, 2020, lookup); err != nil {
		t.Fatal("same camera profiles rejected")
	}
}
