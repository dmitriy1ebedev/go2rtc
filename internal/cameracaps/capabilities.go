// Package cameracaps derives camera capabilities from go2rtc stream names.
// It owns no camera registry and performs no network or device operations.
package cameracaps

import (
	"sort"
	"strings"
)

// Streams contains the identifiers of already configured go2rtc streams.
// It never contains source URLs, device credentials or authentication tokens.
type Streams struct {
	Video string `json:"video"`
	MJPEG string `json:"mjpeg,omitempty"`
	MJPEGMax string `json:"mjpeg_max,omitempty"`
	Audio string `json:"audio,omitempty"`
}

type Features struct {
	PTZ bool `json:"ptz"`
	SoundActivity bool `json:"sound_activity"`
}

// Camera is independent of the client consuming these capabilities.
// Health is "unknown" until a separate, measured health probe is available.
type Camera struct {
	ID string `json:"id"`
	Label string `json:"label"`
	Streams Streams `json:"streams"`
	Features Features `json:"features"`
	Health string `json:"health"`
}

type Options struct {
	TechnicalSuffixes []string
	PTZCamera string
	SoundStream string
}

// ValidName rejects RTSP URLs and unsafe identifiers, matching the legacy
// webOS catalog contract. Camera identities are local stream names only.
func ValidName(s string) bool {
	if s == "" || len(s) >= 100 || strings.ContainsAny(s, "/\\?#@") {
		return false
	}
	for _, c := range s {
		if c < 32 || c == 127 {
			return false
		}
	}
	return true
}

func IsTechnical(name string, suffixes []string) bool {
	if strings.HasSuffix(name, "_pip_max_mjpeg") {
		return true
	}
	for _, suffix := range suffixes {
		if suffix != "" && strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

// Discover creates a fresh bounded snapshot from the existing registry.
// Technical aliases are matched only when those streams already exist.
func Discover(names []string, opts Options) []Camera {
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	all := make(map[string]struct{}, len(sorted))
	for _, name := range sorted {
		if ValidName(name) {
			all[name] = struct{}{}
		}
	}
	cameras := make([]Camera, 0)
	seen := make(map[string]struct{})
	for _, name := range sorted {
		if !ValidName(name) || IsTechnical(name, opts.TechnicalSuffixes) {
			continue
		}
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		if len(cameras) >= 64 {
			break
		}
		cam := Camera{
			ID: name,
			Label: name,
			Streams: Streams{Video: name},
			Features: Features{
				PTZ: name == opts.PTZCamera,
				SoundActivity: name == opts.SoundStream,
			},
			Health: "unknown",
		}
		for _, suffix := range opts.TechnicalSuffixes {
			if !strings.HasSuffix(suffix, "mjpeg") || suffix == "_pip_max_mjpeg" {
				continue
			}
			candidate := name + suffix
			if _, exists := all[candidate]; exists && ValidName(candidate) {
				cam.Streams.MJPEG = candidate
				break
			}
		}
		max := name + "_pip_max_mjpeg"
		if _, exists := all[max]; exists && ValidName(max) {
			cam.Streams.MJPEGMax = max
		}
		audio := name + "_tv_aac"
		if _, exists := all[audio]; exists && ValidName(audio) {
			cam.Streams.Audio = audio
		}
		cameras = append(cameras, cam)
	}
	return cameras
}
