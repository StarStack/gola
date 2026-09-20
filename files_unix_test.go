//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package gola_test

import (
	"net/http/httptest"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestFileStoreRejectsFIFOWithoutBlocking(t *testing.T) {
	store, dir := newTestFileStore(t)
	if err := syscall.Mkfifo(filepath.Join(dir, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := fileTestRouter(store)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/assets/fifo", nil))
		done <- w
	}()
	select {
	case w := <-done:
		if w.Code != 404 {
			t.Fatalf("FIFO response = %d", w.Code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("FIFO request blocked")
	}
}
