package onvif

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPTZStepSendsShortMoveAndStop(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Error("wrong method")
		}
		b := make([]byte, 4096)
		n, _ := r.Body.Read(b)
		body := string(b[:n])
		mu.Lock()
		defer mu.Unlock()
		if strings.Contains(body, "ContinuousMove") {
			calls = append(calls, "move")
			if !strings.Contains(body, `x="0.250" y="0.000"`) {
				t.Error("wrong ONVIF vector")
			}
		} else if strings.Contains(body, "tptz:Stop") {
			calls = append(calls, "stop")
		} else {
			t.Errorf("unexpected SOAP PTZ command")
		}
		w.Header().Set("Content-Type", "application/soap+xml")
		_, _ = w.Write([]byte(`<s:Envelope><s:Body><ok/></s:Body></s:Envelope>`))
	}))
	defer srv.Close()
	u, _ := url.Parse("onvif://user:password@172.31.255.252:2020")
	c := &Client{url: u, ptzURL: srv.URL}
	if err := c.PTZStep("profile1", 0.25, 0, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 || calls[0] != "move" || calls[1] != "stop" {
		t.Fatalf("expected move then stop, got %v", calls)
	}
}

func TestPTZStepAttemptsStopAfterMissingMoveAck(t *testing.T) {
	cases := []struct {
		name       string
		stopStatus int
	}{
		{name: "stop_accepted", stopStatus: http.StatusOK},
		{name: "stop_rejected", stopStatus: http.StatusBadGateway},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var calls []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b := make([]byte, 4096)
				n, _ := r.Body.Read(b)
				body := string(b[:n])
				mu.Lock()
				defer mu.Unlock()
				if strings.Contains(body, "ContinuousMove") {
					calls = append(calls, "move")
					w.WriteHeader(http.StatusBadGateway)
					return
				}
				if strings.Contains(body, "tptz:Stop") {
					calls = append(calls, "stop")
					w.WriteHeader(tc.stopStatus)
					return
				}
				t.Errorf("unexpected PTZ SOAP command")
				w.WriteHeader(http.StatusBadRequest)
			}))
			defer srv.Close()
			u, _ := url.Parse("onvif://127.0.0.1:2020")
			client := &Client{url: u, ptzURL: srv.URL}
			err := client.PTZStep("profile1", 0.25, 0, 100*time.Millisecond)
			if err == nil {
				t.Fatal("failed Move acknowledgement was silently accepted")
			}
			mu.Lock()
			defer mu.Unlock()
			if len(calls) != 2 || calls[0] != "move" || calls[1] != "stop" {
				t.Fatalf("after ambiguous Move expected Stop exactly once; calls=%v", calls)
			}
			if tc.stopStatus != http.StatusOK &&
				!strings.Contains(err.Error(), "emergency PTZ Stop failed") {
				t.Fatalf("Stop failure was hidden from caller: %v", err)
			}
		})
	}
}

// The motor may start even if the camera drops TCP before returning its
// Move acknowledgement. A new independent Stop attempt is mandatory.
func TestPTZStepStopsAfterLostMoveTransportAck(t *testing.T) {
	var moves, stops atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
		if err != nil {
			t.Errorf("read SOAP body: %v", err)
			return
		}
		switch {
		case strings.Contains(string(body), "ContinuousMove"):
			moves.Add(1)
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Error("test HTTP server must support TCP hijacking")
				return
			}
			conn, _, err := hijacker.Hijack()
			if err != nil {
				t.Errorf("drop Move ACK: %v", err)
				return
			}
			_ = conn.Close() // simulate an ambiguous transport failure
		case strings.Contains(string(body), "tptz:Stop"):
			stops.Add(1)
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected PTZ SOAP request")
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()
	u, err := url.Parse("onvif://127.0.0.1:2020")
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{url: u, ptzURL: srv.URL}
	if err := client.PTZStep("profile1", 0.25, 0, 100*time.Millisecond); err == nil {
		t.Fatal("a lost Move acknowledgement must not be treated as success")
	}
	if got := moves.Load(); got != 1 {
		t.Fatalf("expected exactly one Move, got %d", got)
	}
	if got := stops.Load(); got != 1 {
		t.Fatalf("expected exactly one fail-safe Stop, got %d", got)
	}
}

// ONVIF devices regularly redirect service endpoints. Preserve the Go
// HTTP client's upstream redirect behavior rather than attempting to
// classify which camera-supplied redirects are valid (#2732). HTTP 307/308
// preserve SOAP POST; 301/302 follow Go's standard redirect-to-GET behavior.
func TestONVIFSOAPRedirectCompatibility(t *testing.T) {
	for _, tc := range []struct {
		status int
		method string
	}{
		{http.StatusMovedPermanently, http.MethodGet},
		{http.StatusFound, http.MethodGet},
		{http.StatusTemporaryRedirect, http.MethodPost},
		{http.StatusPermanentRedirect, http.MethodPost},
	} {
		for _, operation := range []string{"discovery", "ptz"} {
			t.Run(http.StatusText(tc.status)+"/"+operation, func(t *testing.T) {
				var forwarded atomic.Int32
				destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					forwarded.Add(1)
					if r.Method != tc.method {
						t.Errorf("redirected method=%s, want %s", r.Method, tc.method)
					}
					body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
					if err != nil {
						t.Errorf("cannot read redirected SOAP request: %v", err)
					}
					if tc.method == http.MethodPost {
						if !strings.Contains(string(body), "PasswordDigest") ||
							strings.Contains(string(body), "samplepass") {
							t.Errorf("redirected SOAP must carry digest, not plaintext password")
						}
					} else if len(body) != 0 {
						t.Errorf("redirected GET unexpectedly carried SOAP body")
					}
					w.Header().Set("Content-Type", "application/soap+xml")
					_, _ = w.Write([]byte("<s:Envelope><s:Body><ok/></s:Body></s:Envelope>"))
				}))
				defer destination.Close()
				camera := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodPost {
						t.Errorf("unexpected initial ONVIF method %s", r.Method)
					}
					w.Header().Set("Location", destination.URL+"/onvif/service")
					w.WriteHeader(tc.status)
				}))
				defer camera.Close()
				u, err := url.Parse("onvif://sampleuser:samplepass@127.0.0.1:2020")
				if err != nil {
					t.Fatal(err)
				}
				client := &Client{
					url: u, deviceURL: camera.URL, ptzURL: camera.URL,
					requestTimeout: 2 * time.Second,
				}
				if operation == "ptz" {
					_, err = client.PTZRequest("<tptz:Stop/>")
				} else {
					_, err = client.DeviceRequest(DeviceGetCapabilities)
				}
				if err != nil {
					t.Fatalf("valid ONVIF redirect rejected: %v", err)
				}
				if got := forwarded.Load(); got != 1 {
					t.Fatalf("redirected endpoint received %d requests, want 1", got)
				}
			})
		}
	}
}

func TestPTZFailsClosedAndStopRequiresProfile(t *testing.T) {
	u, _ := url.Parse("onvif://172.31.255.252:2020")
	c := &Client{url: u}
	if err := c.PTZStep("p", 0.25, 0, 100*time.Millisecond); err == nil {
		t.Fatal("missing PTZ service should fail")
	}
	if err := c.PTZStep("p", 1, 0, 200*time.Millisecond); err == nil {
		t.Fatal("unsafe velocity accepted")
	}
	if err := c.PTZStep("p", 0.25, 0, 10*time.Second); err == nil {
		t.Fatal("unsafe move duration accepted")
	}
	if err := c.PTZStop(""); err == nil {
		t.Fatal("empty profile accepted")
	}
}

func TestPTZServiceDiscovery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(
			`<s:Envelope><s:Body><tds:GetCapabilitiesResponse><tds:Capabilities>` +
				`<tt:Media><tt:XAddr>http://127.0.0.1/onvif/media_service</tt:XAddr></tt:Media>` +
				`<tt:PTZ><tt:XAddr>http://127.0.0.1/onvif/ptz_service</tt:XAddr></tt:PTZ>` +
				`</tds:Capabilities></tds:GetCapabilitiesResponse></s:Body></s:Envelope>`))
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	c, err := NewClient("onvif://" + host)
	if err != nil {
		t.Fatal(err)
	}
	if c.ptzURL != srv.URL+"/onvif/ptz_service" {
		t.Fatalf("PTZ endpoint not derived from device capabilities: %s", c.ptzURL)
	}
}

func TestBoundedPTZInitialHandshake(t *testing.T) {
	for _, limit := range []time.Duration{0, -time.Second, 5001 * time.Second} {
		if _, err := NewClientWithTimeout("onvif://127.0.0.1:2020", limit); err == nil {
			t.Fatalf("unsafe timeout %v accepted", limit)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<s:Envelope><s:Body/></s:Envelope>"))
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	c, err := NewClientWithTimeout("onvif://"+host, 4*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if c.requestTimeout != 4*time.Second {
		t.Fatalf("unexpected initial timeout: %v", c.requestTimeout)
	}
}
