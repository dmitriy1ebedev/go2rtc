package camerabench

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestJPEGScannerFindsBoundedFrames(t *testing.T) {
	input := []byte{1, 2, 0xff, 0xd8, 0x11, 0xff, 0xd9, 0, 0xff, 0xd8, 0xff, 0xd9}
	frames, times, err := ReadJPEGFrames(bytes.NewReader(input), 2)
	if err != nil || len(frames) != 2 || len(times) != 2 || len(frames[0]) != 5 || len(frames[1]) != 4 {
		t.Fatalf("invalid JPEG scanner: frames=%d times=%d err=%v", len(frames), len(times), err)
	}
	if !times[1].After(times[0]) && !times[1].Equal(times[0]) {
		t.Fatal("timestamps out of order")
	}
	for i, data := range [][]byte{nil, []byte("not a JPEG"), []byte{0xff, 0xd8, 0x22}} {
		if _, _, err := ReadJPEGFrames(bytes.NewReader(data), 3); err == nil {
			t.Errorf("invalid frame %d accepted", i)
		}
	}
	if _, _, err := ReadJPEGFrames(bytes.NewReader(input), 1); err == nil {
		t.Fatal("frame count bound not enforced")
	}
}

func TestJSONReceiptNeverClaimsGo2RTCEndToEnd(t *testing.T) {
	r := Report{
		Version: "s2-synthetic-v1", Source: "ffmpeg lavfi testsrc2", Isolation: "synthetic_ffmpeg_localhost_only",
		Go2RTCEndToEnd: false, FFmpeg: MediaSample{Frames: 24, MeasuredFPS: 8},
		HTTPClients: ClientSample{Clients: 2, FramesPerClient: 24},
	}
	b, err := JSON(r)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(b) || !bytes.Contains(b, []byte(`"go2rtc_end_to_end": false`)) {
		t.Fatalf("invalid report: %s", b)
	}
	if bytes.Contains(b, []byte("rtsp://")) || bytes.Contains(b, []byte("password")) {
		t.Fatal("credential-bearing data in synthetic report")
	}
}

func TestLocalClientSampleFromKnownJPEGFrames(t *testing.T) {
	jpeg := []byte{0xff, 0xd8, 0xff, 0xd9}
	frames := [][]byte{jpeg, jpeg, jpeg}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	got, err := sampleClients(ctx, frames)
	if err != nil {
		t.Fatal(err)
	}
	if got.Clients != 2 || got.FramesPerClient != 3 || got.MeasuredFPS < 1 || got.GoProcessPeakRSSKiB <= 0 {
		t.Fatalf("invalid synthetic clients: %+v", got)
	}
}
