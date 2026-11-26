package rtsp

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// A camera may accept TCP but reject RTSP DESCRIBE, e.g. while rebooting.
// Both the initial and backchannel-fallback sockets must be closed by go2rtc.
func TestRTSPDescribeFailureClosesConnections(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(fmt.Sprintf("fallback=%v", fallback), func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			n := 1
			suffix := "#backchannel=0"
			if fallback {
				n = 2
				suffix = "#backchannel=1"
			}
			result := make(chan error, n)
			for i := 0; i < n; i++ {
				go func() {
					conn, err := ln.Accept()
					if err != nil {
						result <- err
						return
					}
					defer conn.Close()
					_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
					r := bufio.NewReader(conn)
					line, err := r.ReadString('\n')
					if err != nil || !strings.HasPrefix(line, "DESCRIBE ") {
						result <- fmt.Errorf("unexpected request: %q %v", line, err)
						return
					}
					cseq := "1"
					for {
						line, err = r.ReadString('\n')
						if err != nil {
							result <- err
							return
						}
						if strings.TrimSpace(line) == "" {
							break
						}
						if strings.HasPrefix(strings.ToLower(line), "cseq:") {
							cseq = strings.TrimSpace(line[5:])
						}
					}
					_, err = fmt.Fprintf(conn, "RTSP/1.0 503 Service Unavailable\r\nCSeq: %s\r\nContent-Length: 0\r\n\r\n", cseq)
					if err != nil {
						result <- err
						return
					}
					_, err = r.ReadByte()
					if err != io.EOF {
						result <- fmt.Errorf("failed DESCRIBE socket not closed: %v", err)
						return
					}
					result <- nil
				}()
			}
			producer, err := rtspHandler("rtsp://" + ln.Addr().String() + "/source" + suffix)
			if producer != nil {
				_ = producer.Stop()
				t.Fatal("unexpected producer")
			}
			if err == nil {
				t.Fatal("expected failed DESCRIBE")
			}
			for i := 0; i < n; i++ {
				select {
				case err := <-result:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("camera socket cleanup timeout")
				}
			}
		})
	}
}
