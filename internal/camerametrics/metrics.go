// Package camerametrics exposes an opt-in, read-only view of existing go2rtc
// stream subscriptions. It is independent of the LG webOS client.
package camerametrics

import (
	"net/http"
	"time"

	"github.com/AlexxIT/go2rtc/internal/api"
	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/AlexxIT/go2rtc/internal/cameracaps"
	"github.com/AlexxIT/go2rtc/internal/streams"
)

// Config is disabled by default; no background poller or source is started.
type Config struct {
	Enabled bool `yaml:"enabled"`
}

type CameraMetric struct {
	ID      string                `json:"id"`
	Runtime streams.RuntimeCounts `json:"runtime"`
}

type Snapshot struct {
	Version   string         `json:"version"`
	SampledAt string         `json:"sampled_at"`
	Cameras   []CameraMetric `json:"cameras"`
}

type countReader func(string) (streams.RuntimeCounts, bool)

// Init is called only after the upstream API and streams registry exist.
// No second registry, camera connection, FFmpeg process, or timer is created.
func Init() {
	var cfg struct {
		CameraMetrics Config `yaml:"camera_metrics"`
	}
	app.LoadConfig(&cfg)
	if !cfg.CameraMetrics.Enabled {
		return
	}
	api.HandleFunc("api/cameras/metrics", handler(streams.GetAllNames, func(name string) (streams.RuntimeCounts, bool) {
		stream := streams.Get(name)
		if stream == nil {
			return streams.RuntimeCounts{}, false
		}
		return stream.SnapshotRuntimeCounts(), true
	}))
}

// handler reports configured producer slots and registered subscribers only.
// These are measured counts, NOT verified live FPS, bitrate, first-frame time,
// FFmpeg RSS/CPU, or camera health. Missing signals must never appear as zero.
func handler(getNames func() []string, readCounts countReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// api.origin="*" installs wildcard CORS before this handler.
		// Explicitly clear those inherited headers: Origin is not auth,
		// and this endpoint must never become a cross-origin camera map.
		for _, key := range []string{
			"Access-Control-Allow-Origin",
			"Access-Control-Allow-Methods",
			"Access-Control-Allow-Headers",
			"Access-Control-Expose-Headers",
		} {
			w.Header().Del(key)
		}
		// Server-to-server/same-origin read only. Unlike the LG adapter,
		// no file:// or null origins are allowed.
		w.Header().Set("Vary", "Origin")
		if r.Header.Get("Origin") != "" {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		cams := cameracaps.Discover(getNames(), cameracaps.Options{
			TechnicalSuffixes: []string{"_pip_mjpeg", "_tv_aac"},
		})
		result := Snapshot{
			Version:   "v1",
			SampledAt: time.Now().UTC().Format(time.RFC3339Nano),
			Cameras:   make([]CameraMetric, 0, len(cams)),
		}
		for _, cam := range cams {
			if counters, ok := readCounts(cam.ID); ok {
				result.Cameras = append(result.Cameras, CameraMetric{
					ID:      cam.ID,
					Runtime: counters,
				})
			}
		}
		api.ResponseJSON(w, result)
	}
}
