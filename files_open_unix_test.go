//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package gola

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Exercise the opening primitive separately from the pre-open type check: a
// regular file can be replaced by a FIFO between Lstat and OpenFile.
func TestFileStoreOpenReplacementFIFO(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(name, 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	done := make(chan error, 1)
	go func() {
		file, err := openStoredFile(root, "fifo")
		if file != nil {
			_ = file.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		// Release an accidentally blocking reader before test cleanup.
		writer, _ := os.OpenFile(name, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if writer != nil {
			_ = writer.Close()
		}
		t.Fatal("file opening primitive blocked on a replacement FIFO")
	}
}
