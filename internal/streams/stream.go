package streams

import (
	"encoding/json"
	"sync"
	"sync/atomic"

	"github.com/AlexxIT/go2rtc/pkg/core"
)

type Stream struct {
	producers []*Producer
	consumers []core.Consumer
	mu        sync.Mutex
	pending   atomic.Int32
}

// RuntimeCounts exposes only structural, instantaneous stream counts.
// ProducerSlots are configured slots, not live producer connections.
// It intentionally does not read unsynchronised RTP packet counters,
// source URLs, credentials or per-connection metadata.
type RuntimeCounts struct {
	ProducerSlots        int               `json:"producer_slots"`
	Consumers            int               `json:"consumers"`
	PendingSubscriptions int32             `json:"pending_subscriptions"`
	MJPEGQueue           *MJPEGQueueCounts `json:"mjpeg_queue,omitempty"`
}

// MJPEGQueueCounts covers only live HTTP complete-frame mailboxes of
// currently registered subscribers, not RTP queues or socket buffers.
type MJPEGQueueCounts struct {
	Consumers            int    `json:"consumers"`
	PendingFrames        int    `json:"pending_frames"`
	PendingBytes         int64  `json:"pending_bytes"`
	PeakBytesPerConsumer int    `json:"peak_bytes_per_consumer"`
	DroppedFrames        uint64 `json:"dropped_frames"`
}

type mjpegQueueReader interface {
	SnapshotMJPEGQueue() (pendingBytes, peakBytes int, dropped uint64, ok bool)
}

func (s *Stream) SnapshotRuntimeCounts() RuntimeCounts {
	if s == nil {
		return RuntimeCounts{}
	}
	s.mu.Lock()
	counts := RuntimeCounts{
		ProducerSlots: len(s.producers),
		Consumers:     len(s.consumers),
	}
	for _, consumer := range s.consumers {
		reader, ok := consumer.(mjpegQueueReader)
		if !ok {
			continue
		}
		pending, peak, drops, ok := reader.SnapshotMJPEGQueue()
		if !ok {
			continue
		}
		if counts.MJPEGQueue == nil {
			counts.MJPEGQueue = &MJPEGQueueCounts{}
		}
		q := counts.MJPEGQueue
		q.Consumers++
		if pending > 0 {
			q.PendingFrames++
		}
		q.PendingBytes += int64(pending)
		if peak > q.PeakBytesPerConsumer {
			q.PeakBytesPerConsumer = peak
		}
		q.DroppedFrames += drops
	}
	s.mu.Unlock()
	counts.PendingSubscriptions = s.pending.Load()
	return counts
}

func NewStream(source any) *Stream {
	switch source := source.(type) {
	case string:
		return &Stream{
			producers: []*Producer{NewProducer(source)},
		}
	case []string:
		s := new(Stream)
		for _, str := range source {
			s.producers = append(s.producers, NewProducer(str))
		}
		return s
	case []any:
		s := new(Stream)
		for _, src := range source {
			str, ok := src.(string)
			if !ok {
				log.Error().Msgf("[stream] NewStream: Expected string, got %v", src)
				continue
			}
			s.producers = append(s.producers, NewProducer(str))
		}
		return s
	case map[string]any:
		return NewStream(source["url"])
	case nil:
		return new(Stream)
	default:
		panic(core.Caller())
	}
}

func (s *Stream) Sources() []string {
	sources := make([]string, 0, len(s.producers))
	for _, prod := range s.producers {
		sources = append(sources, prod.url)
	}
	return sources
}

func (s *Stream) SetSource(source string) {
	for _, prod := range s.producers {
		prod.SetSource(source)
	}
}

func (s *Stream) RemoveConsumer(cons core.Consumer) {
	_ = cons.Stop()

	s.mu.Lock()
	for i, consumer := range s.consumers {
		if consumer == cons {
			s.consumers = append(s.consumers[:i], s.consumers[i+1:]...)
			break
		}
	}
	s.mu.Unlock()

	s.stopProducers()
}

func (s *Stream) AddProducer(prod core.Producer) {
	producer := &Producer{conn: prod, state: stateExternal, url: "external"}
	s.mu.Lock()
	s.producers = append(s.producers, producer)
	s.mu.Unlock()
}

func (s *Stream) RemoveProducer(prod core.Producer) {
	s.mu.Lock()
	for i, producer := range s.producers {
		if producer.conn == prod {
			s.producers = append(s.producers[:i], s.producers[i+1:]...)
			break
		}
	}
	s.mu.Unlock()
}

func (s *Stream) stopProducers() {
	if s.pending.Load() > 0 {
		log.Trace().Msg("[streams] skip stop pending producer")
		return
	}

	s.mu.Lock()
producers:
	for _, producer := range s.producers {
		for _, track := range producer.receivers {
			if len(track.Senders()) > 0 {
				continue producers
			}
		}
		for _, track := range producer.senders {
			if len(track.Senders()) > 0 {
				continue producers
			}
		}
		producer.stop()
	}
	s.mu.Unlock()
}

func (s *Stream) MarshalJSON() ([]byte, error) {
	var info = struct {
		Producers []*Producer     `json:"producers"`
		Consumers []core.Consumer `json:"consumers"`
	}{
		Producers: s.producers,
		Consumers: s.consumers,
	}
	return json.Marshal(info)
}
