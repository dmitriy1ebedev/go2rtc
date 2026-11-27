package webospip

import (
	"net/http"
	"net/url"
	"sort"

	"github.com/AlexxIT/go2rtc/internal/api"
)

const maxPTZPolicies = 32

// One controller per configured stream. The legacy endpoint and camera-neutral
// endpoint share the same controller, lock, cooldown and token. Additional
// policies with duplicate names or credentials are rejected, not overwritten.
func buildPTZPolicies(legacy PTZConfig, additional []PTZConfig, suffixes []string, exists func(string) bool) (*ptzController, map[string]*ptzController) {
	controllers := make(map[string]*ptzController)
	accept := func(cfg PTZConfig) *ptzController {
		if !cfg.Enabled || !validName(cfg.Camera) || isTechnical(cfg.Camera, suffixes) || !exists(cfg.Camera) {
			return nil
		}
		return newPTZController(cfg)
	}
	old := accept(legacy)
	if old != nil {
		controllers[old.cfg.Camera] = old
	}
	if len(additional) > maxPTZPolicies {
		return old, controllers
	}
	names := map[string]int{}
	tokens := map[string]int{}
	var candidates []*ptzController
	if old != nil {
		names[old.cfg.Camera]++
		tokens[old.cfg.Token]++
	}
	for _, cfg := range additional {
		if ctl := accept(cfg); ctl != nil {
			names[ctl.cfg.Camera]++
			tokens[ctl.cfg.Token]++
			candidates = append(candidates, ctl)
		}
	}
	for _, ctl := range candidates {
		if names[ctl.cfg.Camera] == 1 && tokens[ctl.cfg.Token] == 1 {
			controllers[ctl.cfg.Camera] = ctl
		}
	}
	return old, controllers
}

func clearPTZHeaders(w http.ResponseWriter) {
	for _, key := range []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Methods", "Access-Control-Allow-Headers", "Access-Control-Expose-Headers", "Access-Control-Allow-Credentials"} {
		w.Header().Del(key)
	}
	w.Header().Set("Cache-Control", "no-store")
}

func routePTZ(controllers map[string]*ptzController) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		clearPTZHeaders(w)
		q, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(q) != 1 || len(q["camera"]) != 1 || !validName(q.Get("camera")) {
			http.Error(w, "invalid camera identity", http.StatusBadRequest)
			return
		}
		ctl := controllers[q.Get("camera")]
		if ctl == nil {
			http.Error(w, "camera PTZ unavailable", http.StatusNotFound)
			return
		}
		ctl.handler(w, r)
	}
}

func registerPTZPolicies(legacy PTZConfig, additional []PTZConfig, suffixes []string, exists func(string) bool) (bool, []string) {
	old, controllers := buildPTZPolicies(legacy, additional, suffixes, exists)
	if old != nil {
		api.HandleFunc("api/webos-pip/ptz", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.RawQuery != "" {
				clearPTZHeaders(w)
				http.Error(w, "legacy endpoint has a fixed camera", http.StatusBadRequest)
				return
			}
			old.handler(w, r)
		})
	}
	ready := make([]string, 0, len(controllers))
	for name := range controllers {
		ready = append(ready, name)
	}
	sort.Strings(ready)
	if len(ready) > 0 {
		api.HandleFunc("api/cameras/ptz", routePTZ(controllers))
	}
	return old != nil, ready
}
