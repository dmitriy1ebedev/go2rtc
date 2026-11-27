package cameracaps

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDiscoverCatalogAndCapabilities(t *testing.T) {
	opts := Options{TechnicalSuffixes: []string{"_pip_mjpeg", "_tv_aac"},
		PTZCamera: "door", SoundStream: "door"}
	input := []string{"cam_b", "door_tv_aac", "door_pip_max_mjpeg", "cam_b",
		"door_pip_mjpeg", "rtsp://user:password@host/live", "door", "bad/name"}
	out := Discover(input, opts)
	if len(out) != 2 || out[0].ID != "cam_b" || out[1].ID != "door" {
		t.Fatalf("unexpected physical camera inventory: %+v", out)
	}
	door := out[1]
	if door.Streams.Video != "door" || door.Streams.MJPEG != "door_pip_mjpeg" ||
		door.Streams.MJPEGMax != "door_pip_max_mjpeg" || door.Streams.Audio != "door_tv_aac" {
		t.Fatalf("missing configured stream aliases: %+v", door)
	}
	if !door.Features.PTZ || !door.Features.SoundActivity || out[0].Features.PTZ {
		t.Fatalf("per-camera features incorrect: %+v", out)
	}
	if door.Health != "unknown" {
		t.Fatal("health must not be guessed without a probe")
	}
	data, err := json.Marshal(out)
	if err != nil { t.Fatal(err) }
	if strings.Contains(string(data), "password") || strings.Contains(string(data), "rtsp://") {
		t.Fatal("stream source credentials exposed")
	}
}

func TestDiscoverDynamicAndBounded(t *testing.T) {
	opts := Options{TechnicalSuffixes: []string{"_pip_mjpeg", "_tv_aac"}}
	if cameras := Discover([]string{"door", "door_pip_mjpeg"}, opts); len(cameras) != 1 ||
		cameras[0].Streams.MJPEG != "door_pip_mjpeg" {
		t.Fatalf("initial camera profile missing: %+v", cameras)
	}
	cameras := Discover([]string{"door", "other", "other_tv_aac"}, opts)
	if len(cameras) != 2 || cameras[0].Streams.MJPEG != "" ||
		cameras[1].Streams.Audio != "other_tv_aac" {
		t.Fatalf("stale or missing stream alias: %+v", cameras)
	}
	many := make([]string, 0, 70)
	for i := 0; i < 70; i++ { many = append(many, strings.Repeat("x", i+1)) }
	if got := Discover(many, opts); len(got) != 64 {
		t.Fatalf("unbounded camera catalog: %d", len(got))
	}
}

func TestNameGuards(t *testing.T) {
	for _, name := range []string{"", "a/b", "a\\b", "a?b", "a#b", "a@b",
		"rtsp://secret@host", "line\nfeed", strings.Repeat("x", 100)} {
		if ValidName(name) { t.Fatalf("unsafe identifier accepted: %q", name) }
	}
	if !ValidName("camera_1") || !IsTechnical("cam_pip_max_mjpeg", nil) ||
		!IsTechnical("cam_tv_aac", []string{"_tv_aac"}) {
		t.Fatal("valid physical or technical name misclassified")
	}
}
