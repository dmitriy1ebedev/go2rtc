package webospip

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/onvif"
)

// The mock is a local HTTP camera. It exercises real pinned SOAP requests;
// the initial resolved client is installed directly, not via a live registry.
func TestPTZStopInterlockRecoversOriginalCamera(t *testing.T) {
	for _, mode := range []string{"stop_rejected", "move_and_stop_rejected", "stop_timeout", "invalid_stop_ack"} {
		t.Run(mode, func(t *testing.T) {
			var moves, stops, discovery atomic.Int32
			var allowStop atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(io.LimitReader(r.Body, 8192))
				if err != nil {
					t.Errorf("read SOAP: %v", err)
					return
				}
				s := string(body)
				switch {
				case strings.Contains(s, "GetCapabilities"):
					discovery.Add(1)
					_, _ = io.WriteString(w, "<Envelope><Body><GetCapabilitiesResponse><Capabilities><PTZ><XAddr>http://127.0.0.1/onvif/ptz_service</XAddr></PTZ></Capabilities></GetCapabilitiesResponse></Body></Envelope>")
				case strings.Contains(s, "<tptz:ContinuousMove>"):
					moves.Add(1)
					if !strings.Contains(s, "<tptz:ProfileToken>original-profile</tptz:ProfileToken>") || !strings.Contains(s, "<tptz:Timeout>PT1S</tptz:Timeout>") {
						t.Error("unsafe or changed movement profile")
					}
					if mode == "move_and_stop_rejected" {
						w.WriteHeader(http.StatusBadGateway)
						return
					}
					_, _ = io.WriteString(w, "<Envelope><Body><ContinuousMoveResponse/></Body></Envelope>")
				case strings.Contains(s, "<tptz:Stop>"):
					n := stops.Add(1)
					if !strings.Contains(s, "<tptz:ProfileToken>original-profile</tptz:ProfileToken>") {
						t.Error("Stop was redirected to a different profile")
					}
					if !allowStop.Load() {
						if mode == "stop_timeout" && n == 1 {
							select {
							case <-r.Context().Done():
							case <-time.After(8 * time.Second):
							}
							return
						}
						if mode == "invalid_stop_ack" {
							_, _ = io.WriteString(w, "<Envelope><Body><ContinuousMoveResponse/></Body></Envelope>")
							return
						}
						w.WriteHeader(http.StatusBadGateway)
						return
					}
					_, _ = io.WriteString(w, "<Envelope><Body><StopResponse/></Body></Envelope>")
				default:
					t.Error("unexpected SOAP request")
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			defer server.Close()
			client, err := onvif.NewPinnedClientWithTimeout("onvif://"+strings.TrimPrefix(server.URL, "http://"), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			ctl := newPTZController(policyConfig("door", "A"))
			ctl.onvifClient, ctl.onvifToken = client, "original-profile"
			// First motor attempt already has a resolved client. All subsequent
			// requests use the production recovery path; even an absent registry
			// entry must not prevent Stop of the original pinned camera.
			ctl.move = ctl.executeMotor
			call := func(direction, token string) int {
				w := httptest.NewRecorder()
				ctl.handler(w, policyRequest("/api/cameras/ptz?camera=door", token, direction))
				return w.Code
			}
			if got := call("up", ctl.cfg.Token); got != http.StatusBadGateway || !ctl.stopUnconfirmed {
				t.Fatalf("unconfirmed Stop not latched: HTTP %d latch=%t", got, ctl.stopUnconfirmed)
			}
			if ctl.onvifClient != client || ctl.onvifToken != "original-profile" || moves.Load() != 1 || stops.Load() != 1 {
				t.Fatal("original recovery identity lost")
			}
			ctl.move = ctl.moveONVIF
			ctl.last = time.Time{} // prove the interlock, not only cooldown
			if got := call("left", ctl.cfg.Token); got != http.StatusBadGateway {
				t.Fatalf("movement admitted while Stop unconfirmed: %d", got)
			}
			if got := call("stop", strings.Repeat("B", 48)); got != http.StatusUnauthorized {
				t.Fatalf("unauthorized recovery admitted: %d", got)
			}
			if got := call("stop", ctl.cfg.Token); got != http.StatusBadGateway || !ctl.stopUnconfirmed {
				t.Fatalf("failed recovery cleared interlock: %d", got)
			}
			allowStop.Store(true)
			if got := call("stop", ctl.cfg.Token); got != http.StatusOK || ctl.stopUnconfirmed {
				t.Fatalf("confirmed Stop did not clear interlock: %d", got)
			}
			if moves.Load() != 1 || stops.Load() != 3 || discovery.Load() != 1 || ctl.onvifClient != nil || ctl.onvifToken != "" {
				t.Fatalf("unexpected SOAP sequence: moves=%d stops=%d discovery=%d", moves.Load(), stops.Load(), discovery.Load())
			}
		})
	}
}

type confirmedStopMotor struct{}

func (confirmedStopMotor) PTZStop(string) error { return nil }
func (confirmedStopMotor) PTZStep(string, float64, float64, time.Duration) error {
	return errors.New("move failed but Stop confirmed")
}

func TestPTZConfirmedStopAfterMoveFailureDoesNotLatch(t *testing.T) {
	ctl := newPTZController(policyConfig("door", "A"))
	ctl.onvifClient, ctl.onvifToken = confirmedStopMotor{}, "profile"
	if err := ctl.executeMotor("up"); err == nil || ctl.stopUnconfirmed || ctl.onvifClient != nil {
		t.Fatal("confirmed Stop must allow later rediscovery, without hiding Move error")
	}
}
