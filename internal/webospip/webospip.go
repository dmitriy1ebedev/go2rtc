// Package webospip adds an optional, read-only LG webOS camera catalog.
// Camera sources remain exclusively in go2rtc's streams registry.
package webospip

import (
	"net/http"
	"strings"
	"time"

	"github.com/AlexxIT/go2rtc/internal/api"
	"github.com/AlexxIT/go2rtc/internal/cameracaps"
	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/AlexxIT/go2rtc/internal/rtsp"
	"github.com/AlexxIT/go2rtc/internal/streams"
)

type Config struct {
	Enabled           bool      `yaml:"enabled"`
	RefreshMS         int       `yaml:"refresh_ms"`
	CatalogPollMS     int       `yaml:"catalog_poll_ms"`
	TechnicalSuffixes []string  `yaml:"technical_suffixes"`
	SoundEnabled      bool      `yaml:"sound_enabled"`
	SoundStream       string    `yaml:"sound_stream"`
	AudioStream       string    `yaml:"audio_stream"`
	ThresholdDBFS     float64   `yaml:"sound_threshold_dbfs"`
	PTZ               PTZConfig `yaml:"ptz"`
}

type camera struct {
	Stream      string `json:"stream"`
	Label       string `json:"label"`
	MJPEGStream string `json:"mjpeg_stream,omitempty"`
	MJPEGMaxStream string `json:"mjpeg_max_stream,omitempty"`
	AudioStream string `json:"audio_stream,omitempty"`
}
type catalog struct {
	PTZEnabled    bool     `json:"ptz_enabled"`
	PTZCamera     string   `json:"ptz_camera,omitempty"`
	Cameras       []camera `json:"cameras"`
	RefreshMS     int      `json:"refresh_ms"`
	CatalogPollMS int      `json:"catalog_poll_ms"`
	SoundEnabled  bool     `json:"sound_enabled"`
	SoundStream   string   `json:"sound_stream,omitempty"`
	AudioStream   string   `json:"audio_stream,omitempty"`
}

// Init registers after api.Init and streams.Init. It doesn't start another
// streaming backend, change camera definitions, or publish source URLs.
func Init() {
	var cfg struct {
		WebOSPIP Config `yaml:"webos_pip"`
	}
	cfg.WebOSPIP.RefreshMS = 240
	cfg.WebOSPIP.CatalogPollMS = 15000
	cfg.WebOSPIP.ThresholdDBFS = -32
	app.LoadConfig(&cfg)
	if !cfg.WebOSPIP.Enabled {
		return
	}
	c := normalizeConfig(cfg.WebOSPIP)
	// Resolve live capabilities before capturing the configuration in the
	// catalog handler. Otherwise clients can see PTZ/sound routes that
	// registerPTZ or the runtime checks actually rejected.
	c = resolveAdvertisedCapabilities(c,
		func(name string) bool { return streams.Get(name) != nil },
		registerPTZ(c.PTZ), rtsp.Port != "")
	if c.SoundEnabled {
		monitor := NewSoundMonitor(c.ThresholdDBFS, 3*time.Second, rtsp.Port)
		api.HandleFunc("api/webos-pip/activity", monitor.Handler)
		go monitor.Run(c.SoundStream)
	}
	api.HandleFunc("api/webos-pip", handler(c, streams.GetAllNames))
	api.HandleFunc("api/cameras/capabilities", capabilitiesHandler(c, streams.GetAllNames))
}

// resolveAdvertisedCapabilities reports only services backed by an enabled
// endpoint and an existing go2rtc stream. It never creates its own registry.
func resolveAdvertisedCapabilities(c Config, streamExists func(string) bool, ptzReady, rtspReady bool) Config {
	c.PTZ.Enabled = c.PTZ.Enabled && ptzReady && streamExists(c.PTZ.Camera)
	if c.AudioStream != "" && !streamExists(c.AudioStream) {
		c.AudioStream = ""
	}
	c.SoundEnabled = c.SoundEnabled && c.SoundStream != "" && rtspReady && streamExists(c.SoundStream)
	if !c.SoundEnabled {
		c.SoundStream = ""
	}
	return c
}

func normalizeConfig(c Config) Config {
	if c.RefreshMS < 100 || c.RefreshMS > 5000 {
		c.RefreshMS = 240
	}
	if c.CatalogPollMS < 5000 || c.CatalogPollMS > 120000 {
		c.CatalogPollMS = 15000
	}
	if c.ThresholdDBFS > -5 || c.ThresholdDBFS < -80 {
		c.ThresholdDBFS = -32
	}
	if !validName(c.AudioStream) || !strings.HasSuffix(c.AudioStream, "_tv_aac") {
		c.AudioStream = ""
	}
	if !validName(c.SoundStream) {
		c.SoundStream = ""
		c.SoundEnabled = false
	}
	if len(c.TechnicalSuffixes) == 0 {
		c.TechnicalSuffixes = []string{"_pip_mjpeg", "_tv_aac"}
	}
	var suffixes []string
	for _, s := range c.TechnicalSuffixes {
		if len(s) > 1 && len(s) < 48 && strings.HasPrefix(s, "_") && validName(s) {
			suffixes = append(suffixes, s)
		}
		if len(suffixes) >= 32 {
			break
		}
	}
	c.TechnicalSuffixes = suffixes
	return c
}

// Legacy validation helpers remain available to PTZ and the webOS adapter.
func validName(s string) bool {
	return cameracaps.ValidName(s)
}

func isTechnical(name string, suffixes []string) bool {
	return cameracaps.IsTechnical(name, suffixes)
}

func modelOptions(c Config) cameracaps.Options {
	options := cameracaps.Options{TechnicalSuffixes: c.TechnicalSuffixes}
	if c.PTZ.Enabled && len(c.PTZ.Token) >= 32 {
		options.PTZCamera = c.PTZ.Camera
	}
	if c.SoundEnabled {
		options.SoundStream = c.SoundStream
	}
	return options
}

// catalogReadOK preserves the legacy read-only method and Origin policy.
// It does not authorize a motor command or expose camera source credentials.
func catalogReadOK(w http.ResponseWriter, r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		if !allowedWebOSPTZOrigin(origin) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return false
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	return true
}

// handler is the stable v1 webOS adapter. The JSON field names and camera
// ordering remain unchanged for already installed televisions.
func handler(c Config, getNames func() []string) http.HandlerFunc {
	c = normalizeConfig(c)
	return func(w http.ResponseWriter, r *http.Request) {
		if !catalogReadOK(w, r) {
			return
		}
		discovered := cameracaps.Discover(getNames(), modelOptions(c))
		cams := make([]camera, 0, len(discovered))
		for _, item := range discovered {
			cams = append(cams, camera{
				Stream: item.Streams.Video,
				Label: item.Label,
				MJPEGStream: item.Streams.MJPEG,
				MJPEGMaxStream: item.Streams.MJPEGMax,
				AudioStream: item.Streams.Audio,
			})
		}
		ptzCamera := ""
		if c.PTZ.Enabled && len(c.PTZ.Token) >= 32 {
			ptzCamera = c.PTZ.Camera
		}
		api.ResponseJSON(w, catalog{
			PTZEnabled: ptzCamera != "", PTZCamera: ptzCamera,
			Cameras: cams, RefreshMS: c.RefreshMS, CatalogPollMS: c.CatalogPollMS,
			SoundEnabled: c.SoundEnabled, SoundStream: c.SoundStream,
			AudioStream: c.AudioStream,
		})
	}
}

type capabilitiesResponse struct {
	Version string `json:"version"`
	Cameras []cameracaps.Camera `json:"cameras"`
}

// capabilitiesHandler provides a client-neutral, versioned read-only view.
// No camera names are persisted; health remains unknown without a live probe.
func capabilitiesHandler(c Config, getNames func() []string) http.HandlerFunc {
	c = normalizeConfig(c)
	return func(w http.ResponseWriter, r *http.Request) {
		if !catalogReadOK(w, r) {
			return
		}
		api.ResponseJSON(w, capabilitiesResponse{
			Version: "v1",
			Cameras: cameracaps.Discover(getNames(), modelOptions(c)),
		})
	}
}
