package logrotate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBasicWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.log")
	w := New(path)
	defer w.Close()

	data := []byte("hello world\n")
	n, err := w.Write(data)
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if n != len(data) {
		t.Errorf("expected %d bytes written, got %d", len(data), n)
	}

	// Close to flush, then read back
	w.Close()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(content) != string(data) {
		t.Errorf("expected %q, got %q", string(data), string(content))
	}
}

func TestRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	w := &Writer{
		Path:     path,
		MaxBytes: 100,
	}
	defer w.Close()

	// Write 80 bytes (under the limit)
	first := make([]byte, 80)
	for i := range first {
		first[i] = 'A'
	}
	n, err := w.Write(first)
	if err != nil {
		t.Fatalf("first Write failed: %v", err)
	}
	if n != 80 {
		t.Errorf("expected 80 bytes written, got %d", n)
	}

	// Write another 70 bytes -- this should trigger rotation since 80+70=150 > 100
	second := make([]byte, 70)
	for i := range second {
		second[i] = 'B'
	}
	n, err = w.Write(second)
	if err != nil {
		t.Fatalf("second Write failed: %v", err)
	}
	if n != 70 {
		t.Errorf("expected 70 bytes written, got %d", n)
	}

	w.Close()

	// The original file should have been rotated to .1
	rotatedPath := path + ".1"
	rotatedContent, err := os.ReadFile(rotatedPath)
	if err != nil {
		t.Fatalf("rotated file not found: %v", err)
	}
	if len(rotatedContent) != 80 {
		t.Errorf("expected rotated file to have 80 bytes, got %d", len(rotatedContent))
	}

	// The current file should have the second write's data
	currentContent, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("current file not found: %v", err)
	}
	if len(currentContent) != 70 {
		t.Errorf("expected current file to have 70 bytes, got %d", len(currentContent))
	}
	// Verify content is all 'B'
	for i, b := range currentContent {
		if b != 'B' {
			t.Errorf("byte %d: expected 'B', got %c", i, b)
			break
		}
	}
}
