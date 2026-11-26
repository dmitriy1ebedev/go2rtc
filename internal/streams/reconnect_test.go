package streams

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/core"
)

func TestReconnectDelayBounded(t *testing.T) {
	for _, tc := range []struct {
		retry int
		want  time.Duration
	}{
		{0, time.Second}, {4, time.Second}, {5, 5 * time.Second}, {9, 5 * time.Second},
		{10, 10 * time.Second}, {19, 10 * time.Second}, {20, time.Minute}, {100, time.Minute},
	} {
		if got := reconnectDelay(tc.retry); got != tc.want {
			t.Errorf("retry %d: %v want %v", tc.retry, got, tc.want)
		}
	}
}

func TestReconnectRepeatedFailureStopsWithConsumer(t *testing.T) {
	const scheme = "test-reconnect-bounded"
	var calls atomic.Int32
	attempt := make(chan time.Time, 8)
	HandleFunc(scheme, func(string) (core.Producer, error) {
		calls.Add(1)
		select {
		case attempt <- time.Now():
		default:
		}
		return nil, errors.New("synthetic source unavailable")
	})
	p := &Producer{url: scheme + ":source", state: stateStart, workerID: 1}
	defer func() { p.stop(); delete(handlers, scheme) }()
	p.reconnect(1, 0)
	var a, b time.Time
	select {
	case a = <-attempt:
	case <-time.After(time.Second):
		t.Fatal("initial retry missing")
	}
	select {
	case b = <-attempt:
	case <-time.After(3 * time.Second):
		t.Fatal("second retry missing")
	}
	if b.Sub(a) < 900*time.Millisecond {
		t.Fatal("retry storm")
	}
	p.stop()
	p.mu.Lock()
	timer, state := p.retryTimer, p.state
	p.mu.Unlock()
	if timer != nil || state != stateNone {
		t.Fatal("stop retained reconnect timer")
	}
	n := calls.Load()
	time.Sleep(1200 * time.Millisecond)
	if calls.Load() != n {
		t.Fatal("reconnect after last consumer stopped")
	}
}

type shortSessionProducer struct {
	core.Connection
}

func (*shortSessionProducer) Start() error { return errors.New("synthetic session terminated") }

func TestReconnectShortSessionsRespectBackoff(t *testing.T) {
	const scheme = "test-reconnect-short-session"
	attempts := make(chan time.Time, 16)
	HandleFunc(scheme, func(string) (core.Producer, error) {
		attempts <- time.Now()
		return &shortSessionProducer{}, nil
	})
	p := &Producer{url: scheme + ":source", state: stateStart, workerID: 1, conn: &shortSessionProducer{}}
	defer func() { p.stop(); delete(handlers, scheme) }()
	start := time.Now()
	// Simulate a dial sequence that has reached the five-second tier.
	// The next successfully dialed but short-lived session must not reset it.
	go p.worker(p.conn, 1, 4)
	var first, second time.Time
	select {
	case first = <-attempts:
	case <-time.After(3 * time.Second):
		t.Fatal("missing first scheduled reconnect")
	}
	if first.Sub(start) < 900*time.Millisecond {
		t.Fatal("immediate reconnect storm")
	}
	select {
	case second = <-attempts:
	case <-time.After(8 * time.Second):
		t.Fatal("missing second scheduled reconnect")
	}
	if second.Sub(first) < 4500*time.Millisecond {
		t.Fatalf("successful dial reset backoff: %v", second.Sub(first))
	}
	p.stop()
	select {
	case <-attempts:
		t.Fatal("retry after stop")
	case <-time.After(1100 * time.Millisecond):
	}
}
