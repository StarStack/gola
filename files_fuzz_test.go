package gola_test

import (
	"mime"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/starstack/gola"
)

func FuzzFileAttachment(f *testing.F) {
	for _, name := range []string{"report.txt", `my "report".txt`, "账单.txt", "\r\nX-Evil: yes", "../escape", "a\\b", "", "bad\xff"} {
		f.Add(name)
	}
	dir := f.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data.txt"), []byte("content"), 0o600); err != nil {
		f.Fatal(err)
	}
	store, err := gola.NewFileStore(dir)
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { _ = store.Close() })
	f.Fuzz(func(t *testing.T, name string) {
		r := gola.New()
		r.GET("/", func(c *gola.Context) {
			if err := c.Attachment(store, "data.txt", name); err != nil {
				if c.Writer.Written() || len(c.Writer.Header()) != 0 {
					t.Fatalf("rejected filename mutated response: %v", err)
				}
				c.Status(400)
			}
		})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
		if w.Code == 400 {
			return
		}
		value := w.Header().Get("Content-Disposition")
		media, params, err := mime.ParseMediaType(value)
		if w.Code != 200 || w.Body.String() != "content" || strings.ContainsAny(value, "\r\n\x00") || err != nil || media != "attachment" || params["filename"] != name {
			t.Fatalf("unsafe disposition for %q: %q (%v)", name, value, err)
		}
	})
}

func FuzzFilePath(f *testing.F) {
	for _, name := range []string{"public.txt", "../secret.txt", "/etc/passwd", ".secret.txt", "a/../public.txt", "a\\b", "C:\\secret.txt", ".", "", "bad\x00"} {
		f.Add(name)
	}
	dir := f.TempDir()
	for name, value := range map[string]string{"public.txt": "public content", ".secret.txt": "private sentinel"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0o600); err != nil {
			f.Fatal(err)
		}
	}
	store, err := gola.NewFileStore(dir)
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { _ = store.Close() })
	f.Fuzz(func(t *testing.T, name string) {
		r := gola.New()
		r.GET("/", func(c *gola.Context) {
			if err := c.File(store, name); err != nil {
				if c.Writer.Written() || strings.Contains(err.Error(), dir) {
					t.Fatalf("failed open leaked state or path: %v", err)
				}
				c.Status(404)
			}
		})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
		if w.Code == 200 && w.Body.String() != "public content" {
			t.Fatalf("unexpected file exposed for %q: %q", name, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "private sentinel") || strings.Contains(w.Body.String(), dir) {
			t.Fatalf("private content exposed for %q", name)
		}
	})
}
