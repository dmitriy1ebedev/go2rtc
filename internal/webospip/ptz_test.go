package webospip

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPTZHandlerRejectsUnsafeRequests(t *testing.T) {
	var calls int32
	ctl := &ptzController{
		cfg: PTZConfig{Camera: "tapo_c230", Token: strings.Repeat("T", 48)},
	}
	ctl.move = func(string) error { atomic.AddInt32(&calls, 1); return nil }
	req := func(method, body, origin, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/webos-pip/ptz", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if token != "" {
			r.Header.Set("X-Go2RTC-PTZ", token)
		}
		w := httptest.NewRecorder()
		ctl.handler(w, r)
		return w
	}
	for _, sample := range []struct {
		method, body, origin, token string
		expected                    int
	}{
		{"GET", "", "", "", http.StatusMethodNotAllowed},
		{"POST", `{"direction":"up"}`, "http://evil.example", strings.Repeat("T", 48), http.StatusForbidden},
		{"POST", `{"direction":"up"}`, "null", "", http.StatusUnauthorized},
		{"POST", `{"direction":"up"}`, "null", strings.Repeat("W", 48), http.StatusUnauthorized},
		{"POST", `{"direction":"diagonal"}`, "null", strings.Repeat("T", 48), http.StatusBadRequest},
		{"POST", `{"direction":"up","host":"8.8.8.8"}`, "null", strings.Repeat("T", 48), http.StatusBadRequest},
		{"POST", `{"direction":"up"}`, "null", strings.Repeat("T", 48), http.StatusOK},
	} {
		w := req(sample.method, sample.body, sample.origin, sample.token)
		if w.Code != sample.expected {
			t.Fatalf("%s %s expected %d got %d", sample.method, sample.body, sample.expected, w.Code)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("unexpected camera commands: %d", got)
	}
	w := req("POST", `{"direction":"left"}`, "null", strings.Repeat("T", 48))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("repeated PTZ commands must be rate-limited: %d", w.Code)
	}
	preflight := req("OPTIONS", "", "null", "")
	if preflight.Code != http.StatusNoContent ||
		preflight.Header().Get("Access-Control-Allow-Origin") != "null" {
		t.Fatal("missing tightly scoped file:// CORS preflight")
	}
}

func TestPTZWebOSFileOriginsAreScopedAndTokenAuthenticated(t *testing.T) {
	const appToken = "TTTTTTTTTTTTTTTTTTTTTTTTTTTTTTTTTTTTTTTTTTTTTTTT"
	var commands int
	ctl := &ptzController{
		cfg: PTZConfig{Camera: "tapo_c230", Token: appToken},
	}
	ctl.move = func(direction string) error {
		if direction != "stop" {
			t.Fatalf("unexpected motor movement: %s", direction)
		}
		commands++
		return nil
	}
	for _, origin := range []string{
		"null", "file://", "file://io.go2rtc.webos.pip",
		"file://io.go2rtc.webos.pip-webos",
	} {
		t.Run(origin, func(t *testing.T) {
			preflight := httptest.NewRequest(http.MethodOptions, "/api/webos-pip/ptz", nil)
			preflight.Header.Set("Origin", origin)
			preflight.Header.Set("Access-Control-Request-Method", "POST")
			preflight.Header.Set("Access-Control-Request-Headers", "content-type,x-go2rtc-ptz")
			response := httptest.NewRecorder()
			ctl.handler(response, preflight)
			if response.Code != http.StatusNoContent ||
				response.Header().Get("Access-Control-Allow-Origin") != origin ||
				!strings.Contains(response.Header().Get("Access-Control-Allow-Headers"), "X-Go2RTC-PTZ") ||
				response.Header().Get("Access-Control-Expose-Headers") != "X-Go2RTC-PTZ-Duration-Ms" {
				t.Fatalf("file-origin preflight rejected: code=%d origin=%q", response.Code, origin)
			}
			for _, test := range []struct {
				token string
				want  int
			}{
				{"", http.StatusUnauthorized},
				{appToken, http.StatusOK},
			} {
				request := httptest.NewRequest(http.MethodPost, "/api/webos-pip/ptz",
					strings.NewReader(`{"direction":"stop"}`))
				request.Header.Set("Origin", origin)
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("X-Go2RTC-PTZ", test.token)
				response := httptest.NewRecorder()
				ctl.handler(response, request)
				if response.Code != test.want ||
					response.Header().Get("Access-Control-Allow-Origin") != origin {
					t.Fatalf("origin=%q token_present=%t code=%d expected=%d",
						origin, test.token != "", response.Code, test.want)
				}
				if test.want == http.StatusOK {
					ms, convErr := strconv.ParseInt(response.Header().Get("X-Go2RTC-PTZ-Duration-Ms"), 10, 64)
					if convErr != nil || ms < 0 {
						t.Fatalf("missing bounded, safe PTZ response timing: %v", convErr)
					}
				}
			}
		})
	}
	for _, origin := range []string{
		"file://evil.app", "file://io.go2rtc.webos.pip.evil", "http://evil.test",
		"https://io.go2rtc.webos.pip", "file://io.go2rtc.webos.pip/evil",
	} {
		request := httptest.NewRequest(http.MethodPost, "/api/webos-pip/ptz",
			strings.NewReader(`{"direction":"stop"}`))
		request.Header.Set("Origin", origin)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Go2RTC-PTZ", appToken)
		response := httptest.NewRecorder()
		ctl.handler(response, request)
		if response.Code != http.StatusForbidden {
			t.Errorf("forbidden origin %q incorrectly accepted: %d", origin, response.Code)
		}
	}
	if commands != 4 {
		t.Fatalf("unexpected authenticated STOP count: %d", commands)
	}
}

func TestPrivateONVIFHostResolution(t *testing.T) {
	local := func(host string) ([]net.IP, error) {
		if host != "camera.lan" {
			t.Fatalf("unexpected lookup %s", host)
		}
		return []net.IP{net.ParseIP("172.31.255.253")}, nil
	}
	cases := []struct {
		host     string
		resolver func(string) ([]net.IP, error)
		want     string
	}{
		{"172.31.255.253", local, "172.31.255.253"},
		{"camera.lan", local, "172.31.255.253"},
		{"8.8.8.8", local, ""},
		{"127.0.0.1", local, ""},
		{"169.254.1.1", local, ""},
		{"tapo-c230.example.org", local, ""},
		{"-camera.lan", local, ""},
		{"bad_.lan", local, ""},
		{"camera.lan", func(string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("172.31.255.253"), net.ParseIP("172.31.255.254")}, nil
		}, ""},
		{"camera.lan", func(string) ([]net.IP, error) { return []net.IP{net.ParseIP("1.1.1.1")}, nil }, ""},
		{"camera.lan", func(string) ([]net.IP, error) { return nil, errors.New("lookup failed") }, ""},
	}
	for _, tt := range cases {
		result, err := privateONVIFIPv4(tt.host, tt.resolver)
		if result != tt.want || (tt.want == "" && err == nil) || (tt.want != "" && err != nil) {
			t.Errorf("host %q returned %q error %v, expected %q", tt.host, result, err, tt.want)
		}
	}
}
