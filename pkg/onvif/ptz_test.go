package onvif

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
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
