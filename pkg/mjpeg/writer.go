package mjpeg

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"
)

func NewWriter(w io.Writer) io.Writer {
	h := w.(http.ResponseWriter).Header()
	h.Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
	return &writer{wr: w, buf: []byte(header), controller: http.NewResponseController(w.(http.ResponseWriter))}
}

const header = "--frame\r\nContent-Type: image/jpeg\r\nContent-Length: "

type writer struct {
	wr         io.Writer
	buf        []byte
	controller *http.ResponseController
}

const frameWriteTimeout = 5 * time.Second

func (w *writer) Write(p []byte) (n int, err error) {
	// Bound a single frame write, not time spent waiting for the source.
	// The native HTTP server supports deadlines; custom wrappers may not.
	if err = w.controller.SetWriteDeadline(time.Now().Add(frameWriteTimeout)); err == nil {
		defer w.controller.SetWriteDeadline(time.Time{})
	} else if !errors.Is(err, http.ErrNotSupported) {
		return 0, err
	}
	w.buf = w.buf[:len(header)]
	w.buf = append(w.buf, strconv.Itoa(len(p))...)
	w.buf = append(w.buf, "\r\n\r\n"...)
	w.buf = append(w.buf, p...)
	w.buf = append(w.buf, "\r\n"...)

	// Chrome bug: mjpeg image always shows the second to last image
	// https://bugs.chromium.org/p/chromium/issues/detail?id=527446
	if n, err = w.wr.Write(w.buf); err != nil {
		return 0, err
	} else if n != len(w.buf) {
		return 0, io.ErrShortWrite
	}

	if err = w.controller.Flush(); err != nil {
		return 0, err
	}

	return len(p), nil
}
