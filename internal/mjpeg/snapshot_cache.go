package mjpeg

import (
	"bytes"
	"net/url"
	"sync"
	"time"

	"github.com/AlexxIT/go2rtc/internal/streams"
)

const (
	snapshotMaxEntries = 32
	snapshotMaxBytes   = 16 << 20
	snapshotMaxFrame   = 2 << 20
	snapshotMaxAge     = 5 * time.Minute
)

type snapshotCacheKey struct {
	stream    *streams.Stream
	transform string
}

// Include only the transformation arguments consumed by JPEGWithQuery.
// Do not retain source URLs, credentials, arbitrary query strings or cache TTL.
// Ambiguous aliases and unusually large options bypass caching entirely.
func snapshotKey(stream *streams.Stream, query url.Values) (snapshotCacheKey, bool) {
	key := snapshotCacheKey{stream: stream}
	if stream == nil {
		return key, false
	}
	options := make(url.Values)
	for _, group := range [][]string{{"width", "w"}, {"height", "h"}, {"rotate"}, {"hardware", "hw"}} {
		for _, name := range group {
			if values, ok := query[name]; ok {
				if len(values) != 1 || len(values[0]) > 128 || len(options[group[0]]) != 0 {
					return key, false
				}
				options.Set(group[0], values[0])
			}
		}
	}
	key.transform = options.Encode()
	return key, true
}

type snapshotCacheEntry struct {
	payload   []byte
	timestamp time.Time
}

type snapshotCache struct {
	mu      sync.Mutex
	entries map[snapshotCacheKey]snapshotCacheEntry
	bytes   int
}

var snapshots snapshotCache

func (c *snapshotCache) remove(key snapshotCacheKey) {
	c.bytes -= len(c.entries[key].payload)
	delete(c.entries, key)
}

func (c *snapshotCache) prune(now time.Time) {
	for key, entry := range c.entries {
		if now.Sub(entry.timestamp) >= snapshotMaxAge {
			c.remove(key)
		}
	}
}

func (c *snapshotCache) get(key snapshotCacheKey, ttl time.Duration, now time.Time) []byte {
	if ttl <= 0 {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prune(now)
	if entry, ok := c.entries[key]; ok && now.Sub(entry.timestamp) < ttl {
		// Immutable stored slice; HTTP response writes do not modify it.
		return entry.payload
	}
	return nil
}

func (c *snapshotCache) put(key snapshotCacheKey, payload []byte, now time.Time) {
	if key.stream == nil || len(payload) < 4 || len(payload) > snapshotMaxFrame ||
		!bytes.HasPrefix(payload, []byte{0xff, 0xd8}) || !bytes.HasSuffix(payload, []byte{0xff, 0xd9}) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prune(now)
	if c.entries == nil {
		c.entries = make(map[snapshotCacheKey]snapshotCacheEntry)
	}
	if old, ok := c.entries[key]; ok {
		if old.timestamp.After(now) {
			return
		}
		c.remove(key)
	}
	for len(c.entries) >= snapshotMaxEntries || c.bytes+len(payload) > snapshotMaxBytes {
		var oldest snapshotCacheKey
		var stamp time.Time
		for k, entry := range c.entries {
			if oldest.stream == nil || entry.timestamp.Before(stamp) {
				oldest, stamp = k, entry.timestamp
			}
		}
		c.remove(oldest)
	}
	// Exact-length copy prevents retaining an oversized producer backing array.
	owned := make([]byte, len(payload))
	copy(owned, payload)
	c.entries[key] = snapshotCacheEntry{payload: owned, timestamp: now}
	c.bytes += len(owned)
}
