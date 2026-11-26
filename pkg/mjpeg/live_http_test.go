package mjpeg

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Constrain the kernel send buffer so the test does not depend on the
// machine's TCP autotuning or whether a large frame fits in a socket buffer.
type smallSendListener struct{ net.Listener }

func (l smallSendListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if err = c.(*net.TCPConn).SetWriteBuffer(4096); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

type httpFrameResult struct {
	elapsed time.Duration
	err     error
}

func TestLiveHTTPWriteDeadline(t *testing.T) {
	started := make(chan struct{})
	result := make(chan httpFrameResult, 1)
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fast" {
			// A second HTTP request must be able to write while the first
			// is blocked; it has an independent frame writer.
			_, _ = NewWriter(w).Write([]byte{0xff, 0xd8, 0xff, 0xd9})
			return
		}
		b := newLiveFrameBuffer()
		_, _ = b.Write(make([]byte, maxLiveFrameBytes))
		start := time.Now()
		close(started)
		_, err := b.writeToContext(r.Context(), NewWriter(w))
		result <- httpFrameResult{time.Since(start), err}
	}))
	s.Listener = smallSendListener{s.Listener}
	s.Start()
	defer s.Close()
	conn, err := net.DialTimeout("tcp", s.Listener.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err = conn.(*net.TCPConn).SetReadBuffer(1024); err != nil {
		t.Fatal(err)
	}
	if _, err = io.WriteString(conn, "GET /slow HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("slow handler not entered")
	}
	// Never read from the first connection. Independently verify a small
	// response can complete on a second connection.
	client := &http.Client{Timeout: 2 * time.Second}
	res, err := client.Get(s.URL + "/fast")
	if err != nil {
		t.Fatal("independent fast client: ", err)
	}
	defer res.Body.Close()
	if _, err = io.ReadAll(res.Body); err != nil || res.StatusCode != 200 {
		t.Fatalf("fast client: HTTP %d, %v", res.StatusCode, err)
	}
	select {
	case r := <-result:
		var ne net.Error
		if !errors.As(r.err, &ne) || !ne.Timeout() {
			t.Fatalf("expected write timeout, got %v", r.err)
		}
		if r.elapsed < frameWriteTimeout-time.Second || r.elapsed > frameWriteTimeout+4*time.Second {
			t.Fatalf("write timeout after %v", r.elapsed)
		}
		t.Logf("blocked socket released after %v; independent fast HTTP client succeeded", r.elapsed)
	case <-time.After(frameWriteTimeout + 5*time.Second):
		t.Fatal("blocked HTTP write did not terminate")
	}
}

func TestLiveHTTPDisconnectWithoutFrames(t *testing.T) {
	entered := make(chan struct{})
	result := make(chan error, 1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := newLiveFrameBuffer()
		close(entered)
		_, err := b.writeToContext(r.Context(), NewWriter(w))
		result <- err
	}))
	defer s.Close()
	conn, err := net.DialTimeout("tcp", s.Listener.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err = io.WriteString(conn, "GET / HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("handler not entered")
	}
	_ = conn.Close()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("expected request cancellation")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("disconnected client retained without frames")
	}
}
