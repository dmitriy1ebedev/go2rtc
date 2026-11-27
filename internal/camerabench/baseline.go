// Package camerabench records a reproducible synthetic, localhost-only
// camera workload. It is not a live camera health probe or go2rtc end-to-end.
package camerabench

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"sort"
	"strconv"
	"syscall"
	"time"
)

const (
	sampleFPS     = 8
	sampleFrames  = 24
	maxJPEGBytes  = 1 << 20
	maxTotalBytes = 16 << 20
)

type MediaSample struct {
	FirstFrameMS      float64 `json:"first_frame_ms"`
	MeasuredFPS       float64 `json:"measured_fps"`
	Frames            int     `json:"frames"`
	FFmpegCPUUserMS   float64 `json:"ffmpeg_cpu_user_ms"`
	FFmpegCPUSystemMS float64 `json:"ffmpeg_cpu_system_ms"`
	FFmpegPeakRSSKiB  int64   `json:"ffmpeg_peak_rss_kib"`
}

type ClientSample struct {
	Clients              int     `json:"clients"`
	FramesPerClient      int     `json:"frames_per_client"`
	FirstFrameMedianMS   float64 `json:"first_frame_median_ms"`
	MeasuredFPS          float64 `json:"measured_fps"`
	GoProcessCPUUserMS   float64 `json:"go_process_cpu_user_ms"`
	GoProcessCPUSystemMS float64 `json:"go_process_cpu_system_ms"`
	GoProcessPeakRSSKiB  int64   `json:"go_process_peak_rss_kib"`
}

type Report struct {
	Version        string       `json:"version"`
	SampledAt      string       `json:"sampled_at"`
	Source         string       `json:"source"`
	Isolation      string       `json:"isolation"`
	Go2RTCEndToEnd bool         `json:"go2rtc_end_to_end"`
	FFmpeg         MediaSample  `json:"ffmpeg"`
	HTTPClients    ClientSample `json:"http_clients"`
}

func millisecond(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
func timevalMS(t syscall.Timeval) float64 { return float64(t.Sec)*1000 + float64(t.Usec)/1000 }
func usage() (syscall.Rusage, error) {
	var r syscall.Rusage
	err := syscall.Getrusage(syscall.RUSAGE_SELF, &r)
	return r, err
}

// ReadJPEGFrames accepts concatenated JPEG frames with a total read bound.
// It never extracts raw private camera URLs, metadata or credentials.
func ReadJPEGFrames(reader io.Reader, limit int) ([][]byte, []time.Time, error) {
	if limit < 1 || limit > 64 {
		return nil, nil, errors.New("invalid frame count bound")
	}
	b := bufio.NewReader(reader)
	frames := make([][]byte, 0, limit)
	stamps := make([]time.Time, 0, limit)
	var buf []byte
	inside := false
	previous := byte(0)
	bytesRead := 0
	for {
		c, err := b.ReadByte()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		bytesRead++
		if bytesRead > maxTotalBytes {
			return nil, nil, errors.New("JPEG stream exceeded byte limit")
		}
		if !inside {
			if previous == 0xff && c == 0xd8 {
				inside = true
				buf = []byte{0xff, 0xd8}
			}
		} else {
			buf = append(buf, c)
			if len(buf) > maxJPEGBytes {
				return nil, nil, errors.New("JPEG frame exceeded byte limit")
			}
			if previous == 0xff && c == 0xd9 {
				frames = append(frames, buf)
				stamps = append(stamps, time.Now())
				buf = nil
				inside = false
				if len(frames) > limit {
					return nil, nil, errors.New("too many JPEG frames")
				}
			}
		}
		previous = c
	}
	if inside {
		return nil, nil, errors.New("truncated JPEG frame")
	}
	if len(frames) == 0 {
		return nil, nil, errors.New("no JPEG frames observed")
	}
	return frames, stamps, nil
}

func runFFmpeg(ctx context.Context) ([][]byte, MediaSample, error) {
	var output MediaSample
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, output, fmt.Errorf("FFmpeg required for synthetic S2 bench: %w", err)
	}
	cmd := exec.CommandContext(ctx, path,
		"-nostdin", "-hide_banner", "-loglevel", "error", "-re",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=8",
		"-frames:v", strconv.Itoa(sampleFrames),
		"-q:v", "12", "-f", "mjpeg", "pipe:1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, output, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, output, err
	}
	frames, stamps, readErr := ReadJPEGFrames(stdout, sampleFrames)
	waitErr := cmd.Wait()
	if readErr != nil {
		return nil, output, fmt.Errorf("read synthetic JPEG: %w", readErr)
	}
	if waitErr != nil {
		return nil, output, fmt.Errorf("synthetic FFmpeg failed: %w (%s)", waitErr, stderr.String())
	}
	if len(frames) != sampleFrames {
		return nil, output, fmt.Errorf("FFmpeg sent %d/%d frames", len(frames), sampleFrames)
	}
	total := stamps[len(stamps)-1].Sub(stamps[0])
	if total <= 0 {
		return nil, output, errors.New("nonpositive FFmpeg sample duration")
	}
	output.FirstFrameMS = millisecond(stamps[0].Sub(start))
	output.MeasuredFPS = float64(len(frames)-1) / total.Seconds()
	output.Frames = len(frames)
	if r, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok {
		output.FFmpegCPUUserMS = timevalMS(r.Utime)
		output.FFmpegCPUSystemMS = timevalMS(r.Stime)
		output.FFmpegPeakRSSKiB = r.Maxrss
	} else {
		return nil, output, errors.New("FFmpeg resource receipt unavailable on this OS")
	}
	if output.FirstFrameMS < 0 || math.IsNaN(output.MeasuredFPS) || output.FFmpegPeakRSSKiB <= 0 {
		return nil, output, errors.New("invalid FFmpeg measurement")
	}
	return frames, output, nil
}

type clientStat struct {
	count       int
	first, span time.Duration
	err         error
}

func sampleClients(ctx context.Context, frames [][]byte) (ClientSample, error) {
	var result ClientSample
	if len(frames) == 0 {
		return result, errors.New("no frames for clients")
	}
	before, err := usage()
	if err != nil {
		return result, err
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
		w.Header().Set("Cache-Control", "no-store")
		f, ok := w.(http.Flusher)
		if !ok {
			return
		}
		period := time.Second / sampleFPS
		for i, frame := range frames {
			if i > 0 {
				timer := time.NewTimer(period)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-r.Context().Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
			if _, err := fmt.Fprintf(w, "--frame\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", len(frame)); err != nil {
				return
			}
			if _, err := w.Write(frame); err != nil {
				return
			}
			if _, err := io.WriteString(w, "\r\n"); err != nil {
				return
			}
			f.Flush()
		}
		_, _ = io.WriteString(w, "--frame--\r\n")
		f.Flush()
	})
	server := httptest.NewServer(handler)
	defer server.Close()
	const clients = 2
	channel := make(chan clientStat, clients)
	for i := 0; i < clients; i++ {
		go func() {
			var stat clientStat
			started := time.Now()
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
			if err != nil {
				stat.err = err
				channel <- stat
				return
			}
			c := http.Client{Timeout: 8 * time.Second}
			response, err := c.Do(request)
			if err != nil {
				stat.err = err
				channel <- stat
				return
			}
			defer response.Body.Close()
			if response.StatusCode != 200 {
				stat.err = fmt.Errorf("HTTP status %d", response.StatusCode)
				channel <- stat
				return
			}
			reader := multipart.NewReader(response.Body, "frame")
			var firstAt, lastAt time.Time
			for {
				part, err := reader.NextPart()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					stat.err = err
					break
				}
				data, err := io.ReadAll(io.LimitReader(part, maxJPEGBytes+1))
				part.Close()
				if err != nil || len(data) < 4 || len(data) > maxJPEGBytes || data[0] != 0xff || data[1] != 0xd8 || data[len(data)-2] != 0xff || data[len(data)-1] != 0xd9 {
					stat.err = errors.New("invalid client JPEG frame")
					break
				}
				now := time.Now()
				if stat.count == 0 {
					firstAt = now
					stat.first = now.Sub(started)
				}
				lastAt = now
				stat.count++
			}
			if stat.count > 1 {
				stat.span = lastAt.Sub(firstAt)
			}
			channel <- stat
		}()
	}
	firstSamples := make([]float64, 0, clients)
	fpsTotal := float64(0)
	for i := 0; i < clients; i++ {
		s := <-channel
		if s.err != nil {
			return result, s.err
		}
		if s.count != len(frames) || s.span <= 0 {
			return result, fmt.Errorf("client frames=%d/%d", s.count, len(frames))
		}
		firstSamples = append(firstSamples, millisecond(s.first))
		fpsTotal += float64(s.count-1) / s.span.Seconds()
	}
	sort.Float64s(firstSamples)
	after, err := usage()
	if err != nil {
		return result, err
	}
	result.Clients = clients
	result.FramesPerClient = len(frames)
	result.FirstFrameMedianMS = (firstSamples[0] + firstSamples[1]) / 2
	result.MeasuredFPS = fpsTotal / clients
	result.GoProcessCPUUserMS = timevalMS(after.Utime) - timevalMS(before.Utime)
	result.GoProcessCPUSystemMS = timevalMS(after.Stime) - timevalMS(before.Stime)
	result.GoProcessPeakRSSKiB = after.Maxrss
	if result.GoProcessPeakRSSKiB <= 0 {
		return result, errors.New("invalid Go RSS measurement")
	}
	return result, nil
}

// Run emits evidence from a synthetic FFmpeg source and two localhost HTTP
// clients only. It must not be called a real-camera or go2rtc E2E benchmark.
func Run(parent context.Context) (Report, error) {
	ctx, cancel := context.WithTimeout(parent, 12*time.Second)
	defer cancel()
	var report Report
	frames, media, err := runFFmpeg(ctx)
	if err != nil {
		return report, err
	}
	clients, err := sampleClients(ctx, frames)
	if err != nil {
		return report, err
	}
	report = Report{
		Version: "s2-synthetic-v1", SampledAt: time.Now().UTC().Format(time.RFC3339),
		Source:         "ffmpeg lavfi testsrc2 160x90 @ 8 fps; 24 MJPEG frames",
		Isolation:      "synthetic_ffmpeg_localhost_only",
		Go2RTCEndToEnd: false,
		FFmpeg:         media, HTTPClients: clients,
	}
	return report, nil
}

// JSON is a stable machine-readable receipt. No access tokens or stream URLs.
func JSON(report Report) ([]byte, error) { return json.MarshalIndent(report, "", "  ") }
