package mjpeg

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/AlexxIT/go2rtc/pkg/core"
)

type snapshotTestProducer struct{ core.Connection }

func (*snapshotTestProducer) Start() error { return nil }

func TestSnapshotFrameBoundedOwnership(t *testing.T) {
	f := newSnapshotFrame(8)
	p := []byte{1, 2, 3}
	_, _ = f.Write(p)
	p[0] = 9
	_, _ = f.Write([]byte{7})
	_ = f.Close()
	got, err := f.wait(context.Background())
	if err != nil || !bytes.Equal(got, []byte{1, 2, 3}) {
		t.Fatalf("capture: %v %v", got, err)
	}
	f = newSnapshotFrame(2)
	_, _ = f.Write(p)
	if _, err = f.wait(context.Background()); !errors.Is(err, errSnapshotFrameTooLarge) {
		t.Fatalf("limit: %v", err)
	}
	f = newSnapshotFrame(2)
	_ = f.Close()
	if _, err = f.wait(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("close: %v", err)
	}
}

func TestSnapshotCaptureTimeoutAndCancel(t *testing.T) {
	for _, cancelEarly := range []bool{false, true} {
		codec := &core.Codec{Name: core.CodecJPEG}
		media := &core.Media{Kind: core.KindVideo, Direction: core.DirectionRecvonly, Codecs: []*core.Codec{codec}}
		track := core.NewReceiver(media, codec)
		producer := &snapshotTestProducer{core.Connection{Medias: []*core.Media{media}, Receivers: []*core.Receiver{track}}}
		stream := streams.NewStream(nil)
		stream.AddProducer(producer)
		ctx, cancel := context.WithCancel(context.Background())
		request := httptest.NewRequest(http.MethodGet, "/api/frame.jpeg", nil).WithContext(ctx)
		done := make(chan error, 1)
		go func() { _, _, err := captureSnapshot(ctx, stream, request, 150*time.Millisecond); done <- err }()
		if cancelEarly {
			deadline := time.Now().Add(100 * time.Millisecond)
			for stream.SnapshotRuntimeCounts().Consumers == 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			cancel()
		}
		select {
		case err := <-done:
			want := context.DeadlineExceeded
			if cancelEarly {
				want = context.Canceled
			}
			if !errors.Is(err, want) {
				t.Fatalf("want %v, got %v", want, err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("snapshot capture hung")
		}
		cancel()
		c := stream.SnapshotRuntimeCounts()
		if c.Consumers != 0 || c.PendingSubscriptions != 0 {
			t.Fatalf("leaked consumer: %+v", c)
		}
		stream.RemoveProducer(producer)
	}
}

func TestSnapshotHTTPAdmissionAndRecovery(t *testing.T) {
	if len(snapshotRequests) != 0 {
		t.Fatal("admission not idle")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type fixture struct {
		stream   *streams.Stream
		producer *snapshotTestProducer
		done     chan int
	}
	var fixtures []fixture
	for i := 0; i < cap(snapshotRequests); i++ {
		codec := &core.Codec{Name: core.CodecJPEG}
		media := &core.Media{Kind: core.KindVideo, Direction: core.DirectionRecvonly, Codecs: []*core.Codec{codec}}
		track := core.NewReceiver(media, codec)
		producer := &snapshotTestProducer{core.Connection{Medias: []*core.Media{media}, Receivers: []*core.Receiver{track}}}
		s := streams.NewStream(nil)
		s.AddProducer(producer)
		done := make(chan int, 1)
		fixtures = append(fixtures, fixture{s, producer, done})
		go func() {
			w := httptest.NewRecorder()
			serveSnapshot(w, httptest.NewRequest(http.MethodGet, "/api/frame.jpeg", nil).WithContext(ctx), s, 5*time.Second)
			done <- w.Code
		}()
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(snapshotRequests) != cap(snapshotRequests) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(snapshotRequests) != cap(snapshotRequests) {
		t.Fatal("snapshot requests did not occupy all slots")
	}
	w := httptest.NewRecorder()
	serveSnapshot(w, httptest.NewRequest(http.MethodGet, "/api/frame.jpeg", nil), streams.NewStream(nil), time.Second)
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != "1" {
		t.Fatalf("expected overload, got %d", w.Code)
	}
	cancel()
	for _, f := range fixtures {
		select {
		case <-f.done:
		case <-time.After(3 * time.Second):
			t.Fatal("canceled request did not exit")
		}
		c := f.stream.SnapshotRuntimeCounts()
		if c.Consumers != 0 || c.PendingSubscriptions != 0 {
			t.Fatalf("leak: %+v", c)
		}
		f.stream.RemoveProducer(f.producer)
	}
	if len(snapshotRequests) != 0 {
		t.Fatal("admission slots leaked")
	}
	// An existing cache entry is available even without a producer.
	s := streams.NewStream(nil)
	r := httptest.NewRequest(http.MethodGet, "/api/frame.jpeg?cache=1s", nil)
	key, ok := snapshotKey(s, r.URL.Query())
	if !ok {
		t.Fatal("invalid fixture key")
	}
	payload := syntheticJPEG(128)
	snapshots.put(key, payload, time.Now())
	w = httptest.NewRecorder()
	serveSnapshot(w, r, s, time.Second)
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), payload) {
		t.Fatalf("cache read: %d", w.Code)
	}
	if len(snapshotRequests) != 0 {
		t.Fatal("cache read occupied acquisition")
	}
	snapshots.mu.Lock()
	snapshots.remove(key)
	snapshots.mu.Unlock()
}
