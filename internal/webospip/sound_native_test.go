package webospip

import (
	"bytes"
	"context"
	"math"
	"testing"
	"time"

	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/pcm"
)

type nativeTestProducer struct{ core.Connection }

func (*nativeTestProducer) Start() error { return nil }

func TestNativeSoundPowerMatchesPCM(t *testing.T) {
	for _, tc := range []struct {
		name   string
		table  *[256]uint64
		decode func(byte) int16
	}{
		{"PCMA", &nativePCMAPower, pcm.PCMAtoPCM},
		{"PCMU", &nativePCMUPower, pcm.PCMUtoPCM},
	} {
		for i := 0; i < 256; i++ {
			x := int64(tc.decode(byte(i)))
			if tc.table[i] != uint64(x*x) {
				t.Fatalf("%s value %d", tc.name, i)
			}
		}
	}
}

func TestNativeSoundExistingStream(t *testing.T) {
	for _, tc := range []struct {
		name        string
		quiet, loud byte
	}{
		{core.CodecPCMA, 0xd5, 0xaa}, {core.CodecPCMU, 0xff, 0x80},
	} {
		t.Run(tc.name, func(t *testing.T) {
			codec := &core.Codec{Name: tc.name, ClockRate: 8000}
			media := &core.Media{Kind: core.KindAudio, Direction: core.DirectionRecvonly, Codecs: []*core.Codec{codec}}
			track := core.NewReceiver(media, codec)
			producer := &nativeTestProducer{core.Connection{Medias: []*core.Media{media}, Receivers: []*core.Receiver{track}}}
			stream := streams.NewStream(nil)
			stream.AddProducer(producer)
			defer stream.RemoveProducer(producer)
			m := NewSoundMonitor(-32, 3*time.Second, "")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan bool, 1)
			go func() { done <- m.sampleNative(ctx, stream) }()
			wait := func(want func() bool) {
				t.Helper()
				deadline := time.Now().Add(3 * time.Second)
				for !want() && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				if !want() {
					t.Fatal("expected monitor state not reached")
				}
			}
			wait(func() bool { return stream.SnapshotRuntimeCounts().Consumers == 1 })
			// Ten standard 20ms audio RTP payloads form one 200ms window.
			send := func(b byte) {
				for i := 0; i < 10; i++ {
					track.Input(&core.Packet{Payload: bytes.Repeat([]byte{b}, 160)})
				}
			}
			send(tc.quiet)
			wait(func() bool { return m.state() == "quiet" })
			send(tc.loud)
			wait(func() bool { return m.state() == "active" })
			cancel()
			select {
			case observed := <-done:
				if !observed {
					t.Fatal("no complete samples")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("native consumer did not stop")
			}
			if c := stream.SnapshotRuntimeCounts(); c.Consumers != 0 || c.PendingSubscriptions != 0 {
				t.Fatalf("consumer leak: %+v", c)
			}
			m.mu.Lock()
			m.lastSample = time.Now().Add(-4 * time.Second)
			m.mu.Unlock()
			if m.state() != "unavailable" {
				t.Fatal("stale samples still available")
			}
		})
	}
}

func TestNativeSoundRejectsUnsupportedTrack(t *testing.T) {
	c := newNativeSoundConsumer(context.Background(), func(float64) {})
	for _, codec := range []*core.Codec{{Name: core.CodecAAC, ClockRate: 8000}, {Name: core.CodecPCMA, ClockRate: 16000}, {Name: core.CodecPCMU, ClockRate: 8000, Channels: 2}} {
		media := &core.Media{Kind: core.KindAudio, Direction: core.DirectionRecvonly, Codecs: []*core.Codec{codec}}
		if err := c.AddTrack(media, codec, core.NewReceiver(media, codec)); err == nil {
			t.Fatal("unsupported track accepted")
		}
	}
	if len(c.Senders) != 0 {
		t.Fatal("unsupported track created sender")
	}
}

func TestNativeSoundLevels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	samples := make(chan float64, 2)
	c := newNativeSoundConsumer(ctx, func(db float64) { samples <- db })
	codec := &core.Codec{Name: core.CodecPCMA, ClockRate: 8000}
	media := &core.Media{Kind: core.KindAudio, Direction: core.DirectionRecvonly, Codecs: []*core.Codec{codec}}
	track := core.NewReceiver(media, codec)
	if err := c.AddTrack(media, codec, track); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		_ = c.Stop()
		for _, sender := range c.Senders {
			sender.Wait()
		}
	}()
	b := byte(0xaa)
	track.Input(&core.Packet{Payload: bytes.Repeat([]byte{b}, nativeSoundSamples)})
	x := int64(pcm.PCMAtoPCM(b))
	want := 20 * math.Log10(math.Abs(float64(x))/32768)
	select {
	case got := <-samples:
		if math.Abs(got-want) > 1e-9 {
			t.Fatalf("got %f want %f", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("missing RMS window")
	}
}

func TestNativeSoundCapabilityWithoutRTSP(t *testing.T) {
	cfg := Config{SoundEnabled: true, SoundNative: true, SoundStream: "door"}
	got := resolveAdvertisedCapabilities(cfg, func(s string) bool { return s == "door" }, false, false)
	if !got.SoundEnabled {
		t.Fatal("native monitor unnecessarily depends on local RTSP listener")
	}
	cfg.SoundNative = false
	got = resolveAdvertisedCapabilities(cfg, func(string) bool { return true }, false, false)
	if got.SoundEnabled {
		t.Fatal("legacy monitor advertised without RTSP")
	}
}
