package streams

import (
	"sync"
	"testing"

	"github.com/AlexxIT/go2rtc/pkg/core"
)

func TestSnapshotRuntimeCountsUsesExistingRegistryState(t *testing.T) {
	if got := (*Stream)(nil).SnapshotRuntimeCounts(); got != (RuntimeCounts{}) {
		t.Fatalf("nil stream must return zero counts: %+v", got)
	}
	stream := NewStream([]string{"rtsp://camera.invalid/live", "ffmpeg:dummy"})
	stream.pending.Store(2)
	stream.mu.Lock()
	stream.consumers = []core.Consumer{nil, nil, nil}
	stream.mu.Unlock()
	got := stream.SnapshotRuntimeCounts()
	if got.ProducerSlots != 2 || got.Consumers != 3 || got.PendingSubscriptions != 2 {
		t.Fatalf("incorrect snapshot: %+v", got)
	}
	if got := stream.SnapshotRuntimeCounts(); got != (RuntimeCounts{ProducerSlots: 2, Consumers: 3, PendingSubscriptions: 2}) {
		t.Fatalf("snapshot changed without a stream mutation: %+v", got)
	}
}

// All structural reads use the Stream mutex; no connection or RTP counters
// are accessed, and no producer is started by this metric.
func TestSnapshotRuntimeCountsConcurrentSubscribers(t *testing.T) {
	stream := NewStream(nil)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			stream.mu.Lock()
			stream.consumers = append(stream.consumers, nil)
			stream.mu.Unlock()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			got := stream.SnapshotRuntimeCounts()
			if got.ProducerSlots != 0 || got.Consumers < 0 || got.Consumers > 200 {
				t.Errorf("impossible count: %+v", got)
			}
		}
	}()
	wg.Wait()
	if got := stream.SnapshotRuntimeCounts(); got.Consumers != 200 {
		t.Fatalf("lost subscribers: %+v", got)
	}
}
