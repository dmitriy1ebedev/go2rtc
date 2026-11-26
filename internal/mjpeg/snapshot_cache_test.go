package mjpeg

import (
	"bytes"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/AlexxIT/go2rtc/internal/streams"
)

func syntheticJPEG(n int) []byte {
	b := make([]byte, n)
	b[0], b[1], b[n-2], b[n-1] = 0xff, 0xd8, 0xff, 0xd9
	return b
}

func TestSnapshotCacheTransformIdentity(t *testing.T) {
	s := streams.NewStream(nil)
	a, ok := snapshotKey(s, url.Values{"src": {"camera"}, "width": {"640"}, "cache": {"1s"}})
	if !ok {
		t.Fatal("valid transform rejected")
	}
	b, ok := snapshotKey(s, url.Values{"src": {"alias"}, "w": {"640"}, "cache": {"2s"}})
	if !ok || a != b {
		t.Fatal("equivalent transform did not match")
	}
	for _, q := range []url.Values{{"width": {"320"}}, {"width": {"640"}, "rotate": {"90"}}, {"width": {"640"}, "hardware": {"vaapi"}}} {
		if k, ok := snapshotKey(s, q); !ok || k == a {
			t.Fatal("distinct transform not isolated")
		}
	}
	for _, q := range []url.Values{{"w": {"640"}, "width": {"320"}}, {"w": {"640", "320"}}, {"hw": {string(make([]byte, 129))}}} {
		if _, ok := snapshotKey(s, q); ok {
			t.Fatal("ambiguous or large option cached")
		}
	}
	other, _ := snapshotKey(streams.NewStream(nil), url.Values{"width": {"640"}})
	if other == a {
		t.Fatal("distinct stream not isolated")
	}
}

func TestSnapshotCacheBoundsAndExpiry(t *testing.T) {
	var c snapshotCache
	now := time.Unix(100, 0)
	first := snapshotCacheKey{stream: streams.NewStream(nil)}
	payload := syntheticJPEG(16)
	c.put(first, payload, now)
	payload[2] = 42
	if got := c.get(first, time.Second, now); len(got) != 16 || got[2] != 0 {
		t.Fatal("cache failed to copy")
	}
	if c.get(first, time.Second, now.Add(time.Second)) != nil {
		t.Fatal("expired TTL served")
	}
	if c.get(first, time.Hour, now.Add(snapshotMaxAge)) != nil || c.bytes != 0 {
		t.Fatal("hard age bound failed")
	}
	c.put(first, []byte{1, 2, 3, 4}, now)
	c.put(first, syntheticJPEG(snapshotMaxFrame+1), now)
	if len(c.entries) != 0 {
		t.Fatal("invalid payload cached")
	}
	for i := 0; i < snapshotMaxEntries+5; i++ {
		k := snapshotCacheKey{stream: streams.NewStream(nil)}
		c.put(k, syntheticJPEG(16), now.Add(time.Duration(i)*time.Millisecond))
	}
	if len(c.entries) != snapshotMaxEntries || c.bytes != snapshotMaxEntries*16 {
		t.Fatal("entry limit failed")
	}
	for i := 0; i < 12; i++ {
		k := snapshotCacheKey{stream: streams.NewStream(nil)}
		c.put(k, syntheticJPEG(snapshotMaxFrame), now.Add(time.Duration(i+100)*time.Millisecond))
	}
	if c.bytes > snapshotMaxBytes || len(c.entries) > snapshotMaxEntries {
		t.Fatal("byte limit failed")
	}
	var sum int
	for _, e := range c.entries {
		sum += len(e.payload)
	}
	if sum != c.bytes {
		t.Fatal("byte accounting mismatch")
	}
}

func TestSnapshotCacheConcurrentAccess(t *testing.T) {
	var c snapshotCache
	key := snapshotCacheKey{stream: streams.NewStream(nil)}
	payload := syntheticJPEG(256)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				c.put(key, payload, time.Now())
				if got := c.get(key, time.Second, time.Now()); got != nil && !bytes.Equal(got, payload) {
					t.Error("payload corruption")
				}
			}
		}()
	}
	wg.Wait()
	if c.bytes != len(payload) || len(c.entries) != 1 {
		t.Fatal("replacement accounting failed")
	}
}
