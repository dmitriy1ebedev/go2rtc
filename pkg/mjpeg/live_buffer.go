package mjpeg

import (
	"context"
	"errors"
	"io"
	"sync"
)

// Limit complete JPEGs, not RTP fragments. One pending frame and one frame
// being written are retained by this mailbox. The upstream codec queues and
// network buffers are separate; this is not a total per-client memory limit.
const maxLiveFrameBytes = 8 << 20

type liveFrameBuffer struct {
	mu      sync.Mutex
	pending []byte
	ready   chan struct{}
	done    chan struct{}
	closed  bool
	claimed bool
	dropped uint64
}

func newLiveFrameBuffer() *liveFrameBuffer {
	return &liveFrameBuffer{ready: make(chan struct{}, 1), done: make(chan struct{})}
}

// Write never waits on the client. Drop older complete frames in preference
// to the newest one, including before the HTTP writer is attached.
func (b *liveFrameBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, io.ErrClosedPipe
	}
	if len(p) == 0 || len(p) > maxLiveFrameBytes {
		b.dropped++
		return len(p), nil
	}
	if b.pending != nil {
		b.dropped++
	}
	// Own the bytes: producers may reuse their backing buffer.
	frame := make([]byte, len(p))
	copy(frame, p)
	b.pending = frame
	select {
	case b.ready <- struct{}{}:
	default:
	}
	return len(p), nil
}

func (b *liveFrameBuffer) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed {
		b.closed = true
		b.pending = nil
		close(b.done)
	}
	return nil
}

func (b *liveFrameBuffer) WriteTo(w io.Writer) (int64, error) {
	return b.writeToContext(context.Background(), w)
}

func (b *liveFrameBuffer) writeToContext(ctx context.Context, w io.Writer) (int64, error) {
	b.mu.Lock()
	if b.claimed {
		b.mu.Unlock()
		return 0, errors.New("MJPEG writer already attached")
	}
	b.claimed = true
	b.mu.Unlock()
	defer b.Close()
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		case <-b.done:
			return total, nil
		case <-b.ready:
		}
		b.mu.Lock()
		frame := b.pending
		b.pending = nil
		closed := b.closed
		b.mu.Unlock()
		if closed {
			return total, nil
		}
		if frame == nil {
			continue
		}
		if err := ctx.Err(); err != nil {
			return total, err
		}
		// Only the handler goroutine uses the HTTP response writer.
		n, err := w.Write(frame)
		total += int64(n)
		if err != nil {
			return total, err
		}
		if n != len(frame) {
			return total, io.ErrShortWrite
		}
	}
}
