package mjpeg

import (
	"context"
	"io"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/pion/rtp"
)

type Consumer struct {
	core.Connection
	wr interface {
		io.WriteCloser
		io.WriterTo
	}
}

func NewConsumer() *Consumer {
	return newConsumer(core.NewWriteBuffer(nil))
}

// NewLiveConsumer is for HTTP MJPEG, where dropping stale complete frames
// is preferable to buffering them behind a slow receiver.
func NewLiveConsumer() *Consumer {
	return newConsumer(newLiveFrameBuffer())
}

func newConsumer(wr interface {
	io.WriteCloser
	io.WriterTo
}) *Consumer {
	medias := []*core.Media{
		{
			Kind:      core.KindVideo,
			Direction: core.DirectionSendonly,
			Codecs: []*core.Codec{
				{Name: core.CodecJPEG},
				{Name: core.CodecRAW},
			},
		},
	}
	return &Consumer{
		Connection: core.Connection{
			ID:         core.NewID(),
			FormatName: "mjpeg",
			Medias:     medias,
			Transport:  wr,
		},
		wr: wr,
	}
}

func (c *Consumer) AddTrack(media *core.Media, _ *core.Codec, track *core.Receiver) error {
	sender := core.NewSender(media, track.Codec)
	sender.Handler = func(packet *rtp.Packet) {
		if n, err := c.wr.Write(packet.Payload); err == nil {
			c.Send += n
		}
	}

	if track.Codec.IsRTP() {
		sender.Handler = RTPDepay(sender.Handler)
	} else if track.Codec.Name == core.CodecRAW {
		sender.Handler = Encoder(track.Codec, 0, sender.Handler)
	}

	sender.HandleRTP(track)
	c.Senders = append(c.Senders, sender)
	return nil
}

func (c *Consumer) WriteTo(wr io.Writer) (int64, error) {
	return c.wr.WriteTo(wr)
}

func (c *Consumer) WriteToContext(ctx context.Context, wr io.Writer) (int64, error) {
	if live, ok := c.wr.(*liveFrameBuffer); ok {
		return live.writeToContext(ctx, wr)
	}
	return c.WriteTo(wr)
}
