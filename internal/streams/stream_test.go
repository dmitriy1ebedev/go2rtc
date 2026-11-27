package streams

import (
	"net/url"
	"testing"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/stretchr/testify/require"
)

// These tests exercise registry operations, not network connections.
// Register their own supported schemes and restore package state afterwards,
// so they run independently of TestApiSchemes and production module Init.
func isolateRegistryTest(t *testing.T, schemes ...string) {
	t.Helper()
	streamsMu.Lock()
	previous := streams
	streams = make(map[string]*Stream)
	streamsMu.Unlock()
	t.Cleanup(func() {
		streamsMu.Lock()
		streams = previous
		streamsMu.Unlock()
	})
	for _, scheme := range schemes {
		old, exists := handlers[scheme]
		HandleFunc(scheme, func(string) (core.Producer, error) {
			t.Error("registry operation unexpectedly dialed a producer")
			return nil, nil
		})
		t.Cleanup(func() {
			if exists {
				handlers[scheme] = old
			} else {
				delete(handlers, scheme)
			}
		})
	}
}

func TestRecursion(t *testing.T) {
	isolateRegistryTest(t, "test-recursion")
	stream1, err := New("from_yaml", "test-recursion:source")
	require.NoError(t, err)
	require.Len(t, streams, 1)

	query, err := url.ParseQuery("src=rtsp://localhost:8554/from_yaml?video")
	require.NoError(t, err)
	stream2, err := GetOrPatch(query)
	require.NoError(t, err)

	require.Same(t, stream1, stream2)
	require.Equal(t, "test-recursion:source", stream2.producers[0].url)
	require.Len(t, streams, 2)
}

func TestTempate(t *testing.T) {
	isolateRegistryTest(t, "ffmpeg", "rtsp")

	stream1, err := New("camera.from_hass", "ffmpeg:{input}#video=copy")
	require.NoError(t, err)
	stream2, err := Patch("camera.from_hass", "rtsp://example.com")
	require.NoError(t, err)

	require.Same(t, stream1, stream2)
	require.Equal(t, "ffmpeg:rtsp://example.com#video=copy", stream1.producers[0].url)
}

func TestRegistryRejectsUnsupportedSource(t *testing.T) {
	isolateRegistryTest(t)
	_, err := New("unsupported", "does_not_matter")
	require.ErrorContains(t, err, "source not supported")
	_, err = Patch("unsupported", "unregistered-test-scheme:source")
	require.ErrorContains(t, err, "source not supported")
	require.Empty(t, streams)
}
