// Package logrotate provides a size-capped log writer.
// When the file reaches maxBytes it is renamed to .1 and a new file is opened.
package logrotate

import (
	"os"
	"sync"
)

const defaultMaxBytes = 5 * 1024 * 1024 // 5 MB

// Writer is an io.Writer that rotates the underlying file when it exceeds MaxBytes.
type Writer struct {
	Path     string
	MaxBytes int64

	mu      sync.Mutex
	file    *os.File
	written int64
}

func New(path string) *Writer {
	return &Writer{Path: path, MaxBytes: defaultMaxBytes}
}

func (w *Writer) open() error {
	f, err := os.OpenFile(w.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	info, _ := f.Stat()
	if info != nil {
		w.written = info.Size()
	}
	w.file = f
	return nil
}

func (w *Writer) rotate() error {
	if w.file != nil {
		w.file.Close()
		w.file = nil
	}
	// Rename current → .1 (overwrite any existing .1)
	_ = os.Rename(w.Path, w.Path+".1")
	w.written = 0
	return w.open()
}

func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		if err := w.open(); err != nil {
			return 0, err
		}
	}
	if w.written+int64(len(p)) > w.MaxBytes {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.file.Write(p)
	w.written += int64(n)
	return n, err
}

func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file != nil {
		err := w.file.Close()
		w.file = nil
		return err
	}
	return nil
}
