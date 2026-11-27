package ffmpeg

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// A Go helper executable lets the resource policy be tested without a camera,
// FFmpeg installation, shell interpreter or platform-specific commands.
func TestSnapshotProcessHelper(t *testing.T) {
	for i, a := range os.Args {
		if a != "--snapshot-test" || i+1 == len(os.Args) {
			continue
		}
		switch os.Args[i+1] {
		case "ok":
			_, _ = os.Stdout.Write([]byte{0xff, 0xd8, 0xff, 0xd9})
			os.Exit(0)
		case "overflow":
			_, _ = os.Stdout.Write(bytes.Repeat([]byte{1}, 65536))
			os.Exit(0)
		case "slow":
			time.Sleep(time.Minute)
			os.Exit(0)
		}
	}
}
func snapshotHelper(mode string) []string {
	return []string{os.Args[0], "-test.run=^TestSnapshotProcessHelper$", "--", "--snapshot-test", mode}
}
func TestSnapshotProcessLimits(t *testing.T) {
	s := snapshotTranscoder{slots: make(chan struct{}, 1), timeout: 3 * time.Second, outputLimit: 1024}
	got, err := s.run(context.Background(), nil, snapshotHelper("ok"))
	if err != nil || !bytes.Equal(got, []byte{0xff, 0xd8, 0xff, 0xd9}) {
		t.Fatalf("success: %x %v", got, err)
	}
	if got, err = s.run(context.Background(), nil, snapshotHelper("overflow")); !errors.Is(err, ErrSnapshotTooLarge) || got != nil {
		t.Fatalf("overflow: %x %v", got, err)
	}
	// Verify slot is released on failure.
	if len(s.slots) != 0 {
		t.Fatal("slot leaked after overflow")
	}
	s.slots <- struct{}{}
	if _, err = s.run(context.Background(), nil, snapshotHelper("ok")); !errors.Is(err, ErrSnapshotBusy) {
		t.Fatalf("admission: %v", err)
	}
	<-s.slots
	if _, err = s.run(context.Background(), make([]byte, snapshotInputLimit+1), snapshotHelper("ok")); !errors.Is(err, ErrSnapshotTooLarge) {
		t.Fatalf("input bound: %v", err)
	}
}
func TestSnapshotProcessTimeoutAndCancellation(t *testing.T) {
	s := snapshotTranscoder{slots: make(chan struct{}, 1), timeout: 150 * time.Millisecond, outputLimit: 1024}
	start := time.Now()
	if _, err := s.run(context.Background(), nil, snapshotHelper("slow")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline: %v", err)
	}
	if time.Since(start) > 3*time.Second || len(s.slots) != 0 {
		t.Fatal("timed out child not promptly reaped")
	}
	s.timeout = 10 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(150*time.Millisecond, cancel)
	defer timer.Stop()
	defer cancel()
	if _, err := s.run(ctx, nil, snapshotHelper("slow")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if len(s.slots) != 0 {
		t.Fatal("slot leaked after cancellation")
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if _, err := s.run(ctx2, nil, snapshotHelper("ok")); !errors.Is(err, context.Canceled) {
		t.Fatalf("already cancelled: %v", err)
	}
}
