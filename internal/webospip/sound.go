package webospip

// Optional sound activity monitor. Runs inside go2rtc, replacing the Python
// 8794 sidecar. No audio is recorded or served to the webOS client.
import (
	"context"
	"encoding/binary"
	"github.com/AlexxIT/go2rtc/internal/api"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"sync"
	"time"
)

type SoundMonitor struct {
	mu          sync.Mutex
	threshold   float64
	hold        time.Duration
	activeUntil time.Time
	lastSample  time.Time
	rtspPort    string
}

func NewSoundMonitor(threshold float64, hold time.Duration, rtspPort string) *SoundMonitor {
	return &SoundMonitor{threshold: threshold, hold: hold, rtspPort: rtspPort}
}
func rmsDBFS(pcm []byte) float64 {
	if len(pcm) < 2 || len(pcm)%2 != 0 {
		return -120
	}
	power := float64(0)
	for i := 0; i+1 < len(pcm); i += 2 {
		x := float64(int16(binary.LittleEndian.Uint16(pcm[i : i+2])))
		power += x * x
	}
	rms := math.Sqrt(power/float64(len(pcm)/2)) / 32768
	if rms <= 0 {
		return -120
	}
	return 20 * math.Log10(rms)
}
func (m *SoundMonitor) observe(db float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	m.lastSample = now
	if db >= m.threshold {
		m.activeUntil = now.Add(m.hold)
	}
}
func (m *SoundMonitor) state() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if time.Since(m.lastSample) > 3500*time.Millisecond {
		return "unavailable"
	}
	if time.Now().Before(m.activeUntil) {
		return "active"
	}
	return "quiet"
}
func (m *SoundMonitor) Handler(w http.ResponseWriter, r *http.Request) {
	// The packaged webOS app uses a file:// origin. Permit only that origin
	// on this read-only activity endpoint, never global wildcard CORS.
	if origin := r.Header.Get("Origin"); origin != "" {
		if !allowedWebOSPTZOrigin(origin) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
	}
	if r.Method != "GET" {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", 405)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	api.ResponseJSON(w, map[string]string{"state": m.state()})
}

// Run retains the existing startup contract. RunContext supports explicit
// shutdown in embedders and prevents unbounded retries when FFmpeg is absent.
func (m *SoundMonitor) Run(stream string) {
	m.RunContext(context.Background(), stream)
}

func soundRetryDelay(failures int) time.Duration {
	if failures <= 0 {
		return 3 * time.Second
	}
	if failures > 4 {
		return 60 * time.Second
	}
	return 3 * time.Second * time.Duration(1<<uint(failures))
}

func (m *SoundMonitor) RunContext(ctx context.Context, stream string) {
	if !validName(stream) || m.rtspPort == "" {
		return
	}
	failures := 0
	for ctx.Err() == nil {
		if m.sampleContext(ctx, stream) {
			failures = 0
		} else {
			failures++
		}
		timer := time.NewTimer(soundRetryDelay(failures))
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-timer.C:
		}
	}
}

func (m *SoundMonitor) sampleContext(parent context.Context, stream string) bool {
	ctx, cancel := context.WithTimeout(parent, 120*time.Second)
	defer cancel()
	source := "rtsp://" + net.JoinHostPort("127.0.0.1", m.rtspPort) + "/" + url.PathEscape(stream)
	cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error",
		"-rtsp_transport", "tcp", "-timeout", "5000000", "-i", source, "-map", "0:a:0", "-vn", "-ac", "1", "-ar", "8000",
		"-f", "s16le", "-acodec", "pcm_s16le", "pipe:1")
	// RTSP socket I/O is bounded separately from the 120s session lifetime.
	cmd.WaitDelay = time.Second
	cmd.Stderr = io.Discard
	cmd.Stdin = nil
	reader, err := cmd.StdoutPipe()
	if err != nil {
		return false
	}
	if err = cmd.Start(); err != nil {
		return false
	}
	frame := make([]byte, 3200)
	hasSamples := false
	for {
		if _, err = io.ReadFull(reader, frame); err != nil {
			break
		}
		hasSamples = true
		m.observe(rmsDBFS(frame))
	}
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	_ = cmd.Wait()
	return hasSamples
}
