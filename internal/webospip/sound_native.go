package webospip

import (
	"context"
	"errors"
	"math"
	"sync/atomic"
	"time"

	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/pcm"
)

const (
	nativeSoundSamples = 1600 // 200ms at 8kHz
	nativeSoundIdle    = 15 * time.Second
)

// Accumulate power directly from compressed samples. Reuse the existing
// G.711 codec implementation; never allocate or retain a PCM audio buffer.
func soundPowerTable(decode func(byte) int16) (out [256]uint64) {
	for i := range out {
		v := int64(decode(byte(i)))
		out[i] = uint64(v * v)
	}
	return
}

var nativePCMAPower = soundPowerTable(pcm.PCMAtoPCM)
var nativePCMUPower = soundPowerTable(pcm.PCMUtoPCM)

type nativeSoundConsumer struct {
	core.Connection
	ctx     context.Context
	observe func(float64)
}

func newNativeSoundConsumer(ctx context.Context, observe func(float64)) *nativeSoundConsumer {
	return &nativeSoundConsumer{
		Connection: core.Connection{ID: core.NewID(), FormatName: "sound-activity",
			Medias: []*core.Media{{Kind: core.KindAudio, Direction: core.DirectionSendonly,
				Codecs: []*core.Codec{{Name: core.CodecPCMA, ClockRate: 8000}, {Name: core.CodecPCMU, ClockRate: 8000}}}}},
		ctx: ctx, observe: observe,
	}
}

func (c *nativeSoundConsumer) AddTrack(media *core.Media, _ *core.Codec, track *core.Receiver) error {
	if len(c.Senders) != 0 || track == nil || track.Codec == nil || track.Codec.ClockRate != 8000 || track.Codec.Channels > 1 {
		return errors.New("unsupported native audio track")
	}
	var table *[256]uint64
	switch track.Codec.Name {
	case core.CodecPCMA:
		table = &nativePCMAPower
	case core.CodecPCMU:
		table = &nativePCMUPower
	default:
		return errors.New("unsupported native audio codec")
	}
	var sum uint64
	var count int
	sender := core.NewSender(media, track.Codec)
	sender.Handler = func(packet *core.Packet) {
		if c.ctx.Err() != nil || len(packet.Payload) > 65536 {
			return
		}
		for _, b := range packet.Payload {
			sum += table[b]
			count++
			if count == nativeSoundSamples {
				db := -120.0
				if sum != 0 {
					db = 10 * math.Log10(float64(sum)/float64(count)/(32768.0*32768.0))
				}
				c.observe(db)
				sum, count = 0, 0
			}
		}
	}
	sender.HandleRTP(track)
	c.Senders = append(c.Senders, sender)
	return nil
}

func (m *SoundMonitor) RunNativeContext(ctx context.Context, name string) {
	if !validName(name) {
		return
	}
	failures := 0
	for ctx.Err() == nil {
		if stream := streams.Get(name); stream != nil && m.sampleNative(ctx, stream) {
			failures = 0
		} else {
			failures++
		}
		timer := time.NewTimer(soundRetryDelay(failures))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (m *SoundMonitor) sampleNative(parent context.Context, stream *streams.Stream) bool {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	if ctx.Err() != nil {
		return false
	}
	samples := make(chan struct{}, 1)
	var hasSamples atomic.Bool
	consumer := newNativeSoundConsumer(ctx, func(db float64) {
		if ctx.Err() != nil {
			return
		}
		hasSamples.Store(true)
		m.observe(db)
		select {
		case samples <- struct{}{}:
		default:
		}
	})
	if err := stream.AddConsumer(consumer); err != nil {
		cancel()
		_ = consumer.Stop()
		return false
	}
	defer func() {
		cancel()
		stream.RemoveConsumer(consumer)
		for _, sender := range consumer.Senders {
			sender.Wait()
		}
	}()
	timer := time.NewTimer(nativeSoundIdle)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return hasSamples.Load()
		case <-timer.C:
			return hasSamples.Load()
		case <-samples:
			timer.Reset(nativeSoundIdle)
		}
	}
}
