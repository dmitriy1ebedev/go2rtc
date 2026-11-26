package mjpeg

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

type controlledWriter struct {
	frames  chan []byte
	release chan struct{}
}

func (w *controlledWriter) Write(p []byte) (int, error) {
	w.frames <- bytes.Clone(p)
	<-w.release
	return len(p), nil
}
func receiveFrame(t *testing.T, w *controlledWriter) []byte {
	t.Helper()
	select {
	case frame := <-w.frames:
		return frame
	case <-time.After(3 * time.Second):
		t.Fatal("writer did not receive frame")
		return nil
	}
}
func TestLiveFrameBufferSlowWriter(t *testing.T) {
	b := newLiveFrameBuffer()
	defer b.Close()
	old := []byte{1}
	_, _ = b.Write(old)
	_, _ = b.Write([]byte{2})
	old[0] = 99
	w := &controlledWriter{make(chan []byte, 2), make(chan struct{}, 2)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := b.writeToContext(ctx, w); done <- err }()
	if got := receiveFrame(t, w); !bytes.Equal(got, []byte{2}) {
		t.Fatalf("startup frame %v", got)
	}
	// The client is blocked inside Write; producing 100 newer frames must
	// finish without waiting for it and without accumulating a FIFO.
	produced := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			_, _ = b.Write([]byte{byte(i)})
		}
		close(produced)
	}()
	select {
	case <-produced:
	case <-time.After(3 * time.Second):
		w.release <- struct{}{}
		t.Fatal("producer blocked by slow client")
	}
	b.mu.Lock()
	pending, dropped := len(b.pending), b.dropped
	b.mu.Unlock()
	if pending != 1 || dropped != 100 {
		t.Errorf("pending=%d dropped=%d", pending, dropped)
	}
	w.release <- struct{}{}
	if got := receiveFrame(t, w); !bytes.Equal(got, []byte{99}) {
		t.Errorf("stale frame %v", got)
	}
	cancel()
	w.release <- struct{}{}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("cancel result %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("writer not released")
	}
}
func TestLiveFrameBufferBoundsAndOwnership(t *testing.T) {
	b := newLiveFrameBuffer()
	defer b.Close()
	p := []byte{4, 5, 6}
	_, _ = b.Write(p)
	p[0] = 9
	_, _ = b.Write(make([]byte, maxLiveFrameBytes+1))
	b.mu.Lock()
	if !bytes.Equal(b.pending, []byte{4, 5, 6}) || b.dropped != 1 {
		t.Error("oversize frame replaced or corrupted valid frame")
	}
	b.mu.Unlock()
	_ = b.Close()
	if _, err := b.Write([]byte{1}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("write after close: %v", err)
	}
	if n, err := b.WriteTo(io.Discard); n != 0 || err != nil {
		t.Fatalf("closed writer: %d %v", n, err)
	}
}
func TestLiveFrameBufferCancelWithoutFrames(t *testing.T) {
	b := newLiveFrameBuffer()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.writeToContext(ctx, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel result %v", err)
	}
	if _, err := b.Write([]byte{1}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal("cancel did not close buffer")
	}
}
