package onvif

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

// Camera-originated redirects must never forward ONVIF authentication
// envelopes to a second endpoint, regardless of HTTP redirect status.
func TestONVIFSOAPRejectsRedirects(t *testing.T) {
	for _, code := range []int{
		http.StatusMovedPermanently,
		http.StatusFound,
		http.StatusTemporaryRedirect,
		http.StatusPermanentRedirect,
	} {
		for _, operation := range []string{"device", "ptz"} {
			t.Run(fmt.Sprintf("%s_%d", operation, code), func(t *testing.T) {
				var forwarded atomic.Int32
				sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					forwarded.Add(1)
					w.WriteHeader(http.StatusOK)
				}))
				defer sink.Close()
				camera := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodPost {
						t.Errorf("camera got %s instead of POST", r.Method)
					}
					w.Header().Set("Location", sink.URL+"/capture")
					w.WriteHeader(code)
				}))
				defer camera.Close()

				identity, _ := url.Parse("onvif://test-user:test-password@127.0.0.1:2020")
				client := &Client{
					url:            identity,
					requestTimeout: 2 * time.Second,
					ptzURL:         camera.URL,
				}
				var err error
				if operation == "device" {
					_, err = client.Request(camera.URL, "<tds:GetCapabilities/>")
				} else {
					_, err = client.PTZRequest("<tptz:Stop/>")
				}
				if err == nil {
					t.Fatal("camera redirect was silently treated as successful SOAP")
				}
				if forwarded.Load() != 0 {
					t.Fatal("ONVIF credentials could be forwarded to a redirect destination")
				}
			})
		}
	}
}
