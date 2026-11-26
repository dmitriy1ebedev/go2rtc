package ffmpeg

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"time"
)

var (
	ErrSnapshotBusy     = errors.New("JPEG snapshot transcoder busy")
	ErrSnapshotTooLarge = errors.New("JPEG snapshot exceeds size limit")
)

const (
	snapshotInputLimit     = 32 << 20
	snapshotOutputLimit    = 16 << 20
	snapshotProcessTimeout = 10 * time.Second
)

type snapshotTranscoder struct {
	slots       chan struct{}
	timeout     time.Duration
	outputLimit int
}

var snapshotProcesses = snapshotTranscoder{
	slots:       make(chan struct{}, 2),
	timeout:     snapshotProcessTimeout,
	outputLimit: snapshotOutputLimit,
}

type snapshotOutput struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
	cancel   context.CancelFunc
}

func (w *snapshotOutput) Write(p []byte) (int, error) {
	if len(p) > w.limit-w.buf.Len() {
		w.overflow = true
		w.cancel()
		return 0, ErrSnapshotTooLarge
	}
	return w.buf.Write(p)
}

// The slot is held until Run has waited for the child and its I/O goroutines.
// The bounds apply to snapshot conversion, not long-running stream producers.
func (s *snapshotTranscoder) run(parent context.Context, input []byte, argv []string) ([]byte, error) {
	if err := parent.Err(); err != nil {
		return nil, err
	}
	if len(input) > snapshotInputLimit {
		return nil, ErrSnapshotTooLarge
	}
	if len(argv) == 0 || argv[0] == "" {
		return nil, errors.New("missing snapshot executable")
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		return nil, ErrSnapshotBusy
	}
	ctx, cancel := context.WithTimeout(parent, s.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.WaitDelay = time.Second
	cmd.Stdin = bytes.NewReader(input)
	cmd.Stderr = io.Discard
	out := snapshotOutput{limit: s.outputLimit, cancel: cancel}
	cmd.Stdout = &out
	err := cmd.Run()
	if out.overflow {
		return nil, ErrSnapshotTooLarge
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	return out.buf.Bytes(), nil
}
