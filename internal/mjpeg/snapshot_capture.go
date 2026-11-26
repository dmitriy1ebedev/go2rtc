package mjpeg

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/AlexxIT/go2rtc/pkg/magic"
)

const (
	snapshotCaptureLimit   = 32 << 20
	snapshotCaptureTimeout = 10 * time.Second
)

// Bound acquisition and response lifetimes independently of the FFmpeg
// conversion limiter. Producer Dial retains its own upstream timeout.
var snapshotRequests = make(chan struct{}, 4)
var errSnapshotFrameTooLarge = errors.New("snapshot frame exceeds acquisition limit")

type capturedFrame struct {
	data []byte
	err  error
}

type snapshotFrame struct {
	once   sync.Once
	result chan capturedFrame
	limit  int
}

func newSnapshotFrame(limit int) *snapshotFrame {
	return &snapshotFrame{result: make(chan capturedFrame, 1), limit: limit}
}

func (f *snapshotFrame) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	f.once.Do(func() {
		if len(p) > f.limit {
			f.result <- capturedFrame{err: errSnapshotFrameTooLarge}
			return
		}
		data := append([]byte(nil), p...)
		f.result <- capturedFrame{data: data}
	})
	return 0, io.EOF
}

func (f *snapshotFrame) Close() error {
	f.once.Do(func() { f.result <- capturedFrame{err: io.EOF} })
	return nil
}

func (f *snapshotFrame) wait(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-f.result:
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return result.data, result.err
	}
}

// No detached AddConsumer goroutine: admission remains held until the
// producer dial and consumer cleanup finish, even if the client disconnects.
func captureSnapshot(parent context.Context, stream *streams.Stream, r *http.Request, timeout time.Duration) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	frame := newSnapshotFrame(snapshotCaptureLimit)
	consumer := magic.NewKeyframeWithWriter(frame)
	consumer.WithRequest(r)
	if err := stream.AddConsumer(consumer); err != nil {
		_ = consumer.Stop()
		return nil, "", err
	}
	defer stream.RemoveConsumer(consumer)
	b, err := frame.wait(ctx)
	return b, consumer.CodecName(), err
}
