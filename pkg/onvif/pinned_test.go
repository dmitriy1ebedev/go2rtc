package onvif

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPinnedONVIFRedirectIsolation(t *testing.T) {
	var foreign atomic.Int32
	outside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { foreign.Add(1); w.WriteHeader(200) }))
	defer outside.Close()
	for _, tc := range []struct {
		name    string
		status  int
		foreign bool
		ok      bool
	}{
		{"foreign", 307, true, false}, {"method change", 302, false, false}, {"same origin", 307, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var local atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/target" {
					local.Add(1)
					if r.Method != "POST" {
						t.Error("SOAP method changed")
					}
					_, _ = w.Write([]byte("<Envelope><Body><StopResponse/></Body></Envelope>"))
					return
				}
				location := "/target"
				if tc.foreign {
					location = outside.URL
				}
				w.Header().Set("Location", location)
				w.WriteHeader(tc.status)
			}))
			defer s.Close()
			u, _ := url.Parse("onvif://user:pass@" + strings.TrimPrefix(s.URL, "http://"))
			c := &Client{url: u, ptzURL: s.URL + "/start", deviceURL: s.URL + "/start", requestTimeout: time.Second, pinnedOrigin: true}
			_, err := c.PTZRequest("<tptz:Stop/>")
			if (err == nil) != tc.ok {
				t.Errorf("PTZ error=%v expected success=%t", err, tc.ok)
			}
			_, err = c.DeviceRequest(DeviceGetCapabilities)
			if (err == nil) != tc.ok {
				t.Errorf("discovery error=%v expected success=%t", err, tc.ok)
			}
			want := int32(0)
			if tc.ok {
				want = 2
			}
			if local.Load() != want {
				t.Errorf("redirect target calls %d want %d", local.Load(), want)
			}
		})
	}
	if foreign.Load() != 0 {
		t.Fatal("SOAP escaped pinned origin")
	}
}

func TestPinnedONVIFMotorAck(t *testing.T) {
	for _, tc := range []struct {
		body string
		ok   bool
	}{
		{"<Envelope><Body><StopResponse/></Body></Envelope>", true},
		{"", false},
		{"<html>OK</html>", false},
		{"<Envelope><Body><ContinuousMoveResponse/></Body></Envelope>", false},
		{"<Envelope><Body><any:Fault/></Body></Envelope>", false},
		{"<Envelope><Body/><Header><StopResponse/></Header></Envelope>", false},
		{"<Envelope><Body><StopResponse/><Fault/></Body></Envelope>", false},
	} {
		if got := validMotorAck([]byte(tc.body), "StopResponse"); got != tc.ok {
			t.Errorf("ack=%q got=%t want=%t", tc.body, got, tc.ok)
		}
	}
}

func TestBoundedSOAPRejectsOversize(t *testing.T) {
	if _, err := boundedSOAPBody(strings.NewReader(strings.Repeat("x", (256<<10)+1))); err == nil {
		t.Fatal("oversized response accepted")
	}
}
