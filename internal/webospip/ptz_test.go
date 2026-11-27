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

func TestPTZRejectsAmbiguousOrOversizedJSONWithoutMotorCommands(t *testing.T) {
	token := strings.Repeat("T", 48)
	var commands atomic.Int32
	ctl := &ptzController{cfg: PTZConfig{Camera: "door", Token: token}}
	ctl.move = func(direction string) error {
		commands.Add(1)
		if direction != "stop" {
			t.Errorf("unexpected accepted direction: %q", direction)
		}
		return nil
	}
	cases := []struct {
		name, contentType, body string
		want int
	}{
		{"combined direction", "application/json", `{"direction":"up|down"}`, http.StatusBadRequest},
		{"duplicate direction", "application/json", `{"direction":"up","direction":"down"}`, http.StatusBadRequest},
		{"foreign host", "application/json", `{"direction":"up","host":"8.8.8.8"}`, http.StatusBadRequest},
		{"trailing object", "application/json", `{"direction":"up"}{"direction":"stop"}`, http.StatusBadRequest},
		{"trailing literal", "application/json", `{"direction":"up"} false`, http.StatusBadRequest},
		{"missing direction", "application/json", `{}`, http.StatusBadRequest},
		{"uppercase key", "application/json", `{"Direction":"up"}`, http.StatusBadRequest},
		{"uppercase command", "application/json", `{"direction":"UP"}`, http.StatusBadRequest},
		{"nonstring direction", "application/json", `{"direction":1}`, http.StatusBadRequest},
		{"null direction", "application/json", `{"direction":null}`, http.StatusBadRequest},
		{"array", "application/json", `[{"direction":"up"}]`, http.StatusBadRequest},
		{"empty body", "application/json", "", http.StatusBadRequest},
		{"incomplete body", "application/json", `{"direction":"up"`, http.StatusBadRequest},
		{"oversized body", "application/json", `{"direction":"up","extra":"` + strings.Repeat("x", 170) + `"}`, http.StatusBadRequest},
		{"spoofed JSON media type", "application/json-evil", `{"direction":"stop"}`, http.StatusUnsupportedMediaType},
		{"unsupported charset", "application/json; charset=iso-8859-1", `{"direction":"stop"}`, http.StatusUnsupportedMediaType},
		{"missing media type", "", `{"direction":"stop"}`, http.StatusUnsupportedMediaType},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/webos-pip/ptz", strings.NewReader(tc.body))
			r.Header.Set("Origin", "null")
			r.Header.Set("X-Go2RTC-PTZ", token)
			if tc.contentType != "" {
				r.Header.Set("Content-Type", tc.contentType)
			}
			rec := httptest.NewRecorder()
			ctl.handler(rec, r)
			if rec.Code != tc.want {
				t.Fatalf("status %d expected %d for %q", rec.Code, tc.want, tc.name)
			}
		})
	}
	if got := commands.Load(); got != 0 {
		t.Fatalf("invalid request reached the motor %d times", got)
	}
	// The installed LG client sends this same single-object JSON shape.
	valid := httptest.NewRequest(http.MethodPost, "/api/webos-pip/ptz",
		strings.NewReader(`{"direction":"stop"}`))
	valid.Header.Set("Origin", "file://io.go2rtc.webos.pip")
	valid.Header.Set("Content-Type", "application/json; charset=utf-8")
	valid.Header.Set("X-Go2RTC-PTZ", token)
	rec := httptest.NewRecorder()
	ctl.handler(rec, valid)
	if rec.Code != http.StatusOK || commands.Load() != 1 {
		t.Fatalf("valid LG stop rejected or not delivered: code=%d commands=%d",
			rec.Code, commands.Load())
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
		if host != "tapo-c230.lan" {
			t.Fatalf("unexpected lookup %s", host)
		}
		return []net.IP{net.ParseIP("192.168.0.63")}, nil
	}
	cases := []struct {
		host     string
		resolver func(string) ([]net.IP, error)
		want     string
	}{
		{"192.168.0.63", local, "192.168.0.63"},
		{"tapo-c230.lan", local, "192.168.0.63"},
		{"8.8.8.8", local, ""},
		{"127.0.0.1", local, ""},
		{"169.254.1.1", local, ""},
		{"tapo-c230.example.org", local, ""},
		{"-camera.lan", local, ""},
		{"bad_.lan", local, ""},
		{"tapo-c230.lan", func(string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("192.168.0.63"), net.ParseIP("192.168.0.64")}, nil
		}, ""},
		{"tapo-c230.lan", func(string) ([]net.IP, error) { return []net.IP{net.ParseIP("1.1.1.1")}, nil }, ""},
		{"tapo-c230.lan", func(string) ([]net.IP, error) { return nil, errors.New("lookup failed") }, ""},
	}
	for _, tt := range cases {
		result, err := privateONVIFIPv4(tt.host, tt.resolver)
		if result != tt.want || (tt.want == "" && err == nil) || (tt.want != "" && err != nil) {
			t.Errorf("host %q returned %q error %v, expected %q", tt.host, result, err, tt.want)
		}
	}
}
