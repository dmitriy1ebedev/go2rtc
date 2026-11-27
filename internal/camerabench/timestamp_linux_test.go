//go:build linux

package camerabench

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"sort"
	"sync"
	"testing"
	"time"
)

const (
	markerWidth    = 160
	markerHeight   = 90
	markerCapacity = 2048
)

// A bounded synthetic timestamp registry. The sequence and its complement
// survive H264->JPEG; no wall clock synchronisation or camera data required.
type timedFixture struct {
	mu   sync.Mutex
	sent [markerCapacity]time.Time
}

func markerRGB(seq int) []byte {
	frame := make([]byte, markerWidth*markerHeight*3)
	for y := 0; y < markerHeight; y++ {
		for x := 0; x < markerWidth; x++ {
			bit := (seq >> uint(x/10)) & 1
			if y >= 45 {
				bit ^= 1
			}
			v := byte(24 + bit*208)
			i := (y*markerWidth + x) * 3
			frame[i], frame[i+1], frame[i+2] = v, v, v
		}
	}
	return frame
}

func markerSequence(img image.Image) (int, error) {
	b := img.Bounds()
	if b.Dx() != markerWidth || b.Dy() != markerHeight {
		return 0, fmt.Errorf("unexpected marker dimensions")
	}
	var seq int
	for bit := 0; bit < 16; bit++ {
		a, _, _, _ := img.At(b.Min.X+bit*10+5, b.Min.Y+20).RGBA()
		z, _, _, _ := img.At(b.Min.X+bit*10+5, b.Min.Y+65).RGBA()
		if a > 48000 && z < 17000 {
			seq |= 1 << uint(bit)
		} else if !(a < 17000 && z > 48000) {
			return 0, fmt.Errorf("invalid complementary marker bit")
		}
	}
	if seq <= 0 || seq >= markerCapacity {
		return 0, fmt.Errorf("marker out of range")
	}
	return seq, nil
}

func (f *timedFixture) feed(ctx context.Context, w io.WriteCloser) error {
	defer w.Close()
	ticker := time.NewTicker(time.Second / 8)
	defer ticker.Stop()
	for seq := 1; seq < markerCapacity; seq++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		frame := markerRGB(seq)
		f.mu.Lock()
		f.sent[seq] = time.Now()
		f.mu.Unlock()
		if n, err := w.Write(frame); err != nil {
			return err
		} else if n != len(frame) {
			return io.ErrShortWrite
		}
	}
	return fmt.Errorf("marker sequence exhausted")
}

type fixtureLatency struct {
	samples []float64
	lastSeq int
	gaps    int
	err     error
}

func (f *timedFixture) observe(l *fixtureLatency, data []byte) {
	if l.err != nil {
		return
	}
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		l.err = err
		return
	}
	seq, err := markerSequence(img)
	if err != nil {
		l.err = err
		return
	}
	if seq <= l.lastSeq {
		l.err = fmt.Errorf("non-increasing source marker %d after %d", seq, l.lastSeq)
		return
	}
	if l.lastSeq != 0 {
		l.gaps += seq - l.lastSeq - 1
	}
	l.lastSeq = seq
	f.mu.Lock()
	sent := f.sent[seq]
	f.mu.Unlock()
	if sent.IsZero() {
		l.err = fmt.Errorf("unknown source marker %d", seq)
		return
	}
	l.samples = append(l.samples, millisecond(time.Since(sent)))
}
func (l *fixtureLatency) report() map[string]any {
	sort.Float64s(l.samples)
	n := len(l.samples)
	if n == 0 {
		return nil
	}
	return map[string]any{"samples": n, "min_ms": l.samples[0], "p50_ms": l.samples[(n-1)/2], "p95_ms": l.samples[(95*n+99)/100-1], "p99_ms": l.samples[(99*n+99)/100-1], "max_ms": l.samples[n-1], "source_sequence_gaps": l.gaps}
}
func TestMarkerSequenceJPEG(t *testing.T) {
	for _, seq := range []int{1, 15, 256, 1023, 2047} {
		raw := markerRGB(seq)
		img := image.NewRGBA(image.Rect(0, 0, markerWidth, markerHeight))
		for i := 0; i < len(raw)/3; i++ {
			copy(img.Pix[i*4:i*4+3], raw[i*3:i*3+3])
			img.Pix[i*4+3] = 255
		}
		var b bytes.Buffer
		if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 60}); err != nil {
			t.Fatal(err)
		}
		decoded, err := jpeg.Decode(&b)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := markerSequence(decoded); err != nil || got != seq {
			t.Fatalf("marker got %d want %d err=%v", got, seq, err)
		}
	}
}
