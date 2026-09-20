package gola_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/starstack/gola"
)

func newTestFileStore(t *testing.T) (*gola.FileStore, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := gola.NewFileStore(dir)
	if errors.Is(err, gola.ErrFileStorePlatform) {
		t.Skip("file store platform unsupported")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, dir
}

func storedUpload(t *testing.T, content string) *multipart.FileHeader {
	t.Helper()
	body, media := makeMultipartPayload(t, nil, []multipartTestFile{{"file", "../../client-chosen.txt", content}})
	req := httptest.NewRequest(http.MethodPost, "/upload", bytes.NewReader(body))
	req.Header.Set("Content-Type", media)
	if err := req.ParseMultipartForm(1 << 20); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = req.MultipartForm.RemoveAll() })
	return req.MultipartForm.File["file"][0]
}

func requireNoFileStaging(t *testing.T, dir string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(entry.Name(), ".gola-upload-") {
			t.Errorf("staging file retained: %s", name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestFileStoreSaveLimitsAndNoOverwrite(t *testing.T) {
	store, dir := newTestFileStore(t)
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	file := storedUpload(t, "hello")
	file.Size = 1 // A caller can mutate metadata; the actual stream must be limited.
	if err := store.Save(context.Background(), file, "nested/saved.txt", 5); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "nested/saved.txt"))
	if err != nil || string(got) != "hello" {
		t.Fatalf("saved content = %q, %v", got, err)
	}
	info, err := os.Stat(filepath.Join(dir, "nested/saved.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("file permissions = %v", info.Mode())
	}
	if err := store.Save(context.Background(), storedUpload(t, "replacement"), "nested/saved.txt", 100); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("overwrite error = %v", err)
	}
	got, _ = os.ReadFile(filepath.Join(dir, "nested/saved.txt"))
	if string(got) != "hello" {
		t.Fatalf("existing file changed: %q", got)
	}
	if err := store.Save(context.Background(), file, "too-large.txt", 4); !errors.Is(err, gola.ErrFileTooLarge) {
		t.Fatalf("size limit error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "too-large.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("failed save retained destination: %v", err)
	}
	if err := store.Save(context.Background(), file, "huge-limit.txt", math.MaxInt64); err != nil {
		t.Fatalf("max limit overflow: %v", err)
	}
	if err := store.Save(context.Background(), storedUpload(t, ""), "empty.txt", 1); err != nil {
		t.Fatal(err)
	}
	requireNoFileStaging(t, dir)
}

func TestFileStoreInvalidInputs(t *testing.T) {
	store, dir := newTestFileStore(t)
	file := storedUpload(t, "hello")
	for _, name := range []string{"", ".", "..", "../escape", "/absolute", "a/../b", "a//b", "a/./b", "a/", "a\\b", "C:/file", "a:b", ".env", ".git/config", "a/.secret", "a\x00b", "a\r\nb", "trailing.", "trailing ", "NUL", "com1.txt", "a\x7fb", "bad\xff"} {
		t.Run(name, func(t *testing.T) {
			if err := store.Save(context.Background(), file, name, 100); !errors.Is(err, gola.ErrInvalidFilePath) {
				t.Fatalf("Save(%q) = %v", name, err)
			}
		})
	}
	for _, limit := range []int64{-1, 0} {
		if err := store.Save(context.Background(), file, "file", limit); !errors.Is(err, gola.ErrInvalidFileLimit) {
			t.Fatalf("limit %d: %v", limit, err)
		}
	}
	if err := store.Save(context.Background(), nil, "file", 10); !errors.Is(err, gola.ErrInvalidUpload) {
		t.Fatalf("nil upload: %v", err)
	}
	if err := store.Save(context.Background(), &multipart.FileHeader{}, "unreadable", 10); !errors.Is(err, gola.ErrInvalidUpload) {
		t.Fatalf("unreadable upload: %v", err)
	}
	if err := store.Save(nil, file, "file", 10); !errors.Is(err, gola.ErrInvalidUpload) {
		t.Fatalf("nil context: %v", err)
	}
	if err := store.Save(context.Background(), file, "missing/file", 10); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing parent: %v", err)
	}
	if _, err := gola.NewFileStore(filepath.Join(dir, "missing")); !errors.Is(err, fs.ErrNotExist) || strings.Contains(err.Error(), dir) {
		t.Fatalf("constructor leaked path or unexpected error: %v", err)
	}
	requireNoFileStaging(t, dir)
}

type cancelDuringFileCopy struct {
	context.Context
	cancel context.CancelFunc
	checks int
}

func (c *cancelDuringFileCopy) Err() error {
	c.checks++
	if c.checks >= 3 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestFileStoreSaveCancellationCleansPartial(t *testing.T) {
	store, dir := newTestFileStore(t)
	file := storedUpload(t, strings.Repeat("data", 64<<10))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Save(ctx, file, "before.txt", 1<<20); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled error = %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	during := &cancelDuringFileCopy{Context: ctx, cancel: cancel}
	if err := store.Save(during, file, "during.txt", 1<<20); !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-copy canceled error = %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed saves left files: %v, %v", entries, err)
	}
}

func TestFileStoreConcurrentSave(t *testing.T) {
	store, dir := newTestFileStore(t)
	file := storedUpload(t, "winner")
	var group sync.WaitGroup
	var successes atomic.Int32
	for range 12 {
		group.Go(func() {
			err := store.Save(context.Background(), file, "same.txt", 100)
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, fs.ErrExist) {
				t.Errorf("concurrent save: %v", err)
			}
		})
	}
	group.Wait()
	if successes.Load() != 1 {
		t.Fatalf("successful writers = %d", successes.Load())
	}
	got, err := os.ReadFile(filepath.Join(dir, "same.txt"))
	if err != nil || string(got) != "winner" {
		t.Fatalf("published file = %q, %v", got, err)
	}
	requireNoFileStaging(t, dir)
}

func fileTestRouter(store *gola.FileStore) *gola.Engine {
	r := gola.New()
	r.GET("/assets/*path", store.Static("path"))
	r.HEAD("/assets/*path", store.Static("path"))
	return r
}

func TestFileStoreStaticHTTP(t *testing.T) {
	store, dir := newTestFileStore(t)
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello world"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := fileTestRouter(store)
	for _, tc := range []struct {
		name, method, rangeHeader string
		status, length            int
		body                      string
	}{
		{"full", "GET", "", 200, 11, "hello world"},
		{"head", "HEAD", "", 200, 11, ""},
		{"range", "GET", "bytes=0-4", 206, 5, "hello"},
		{"suffix", "GET", "bytes=-5", 206, 5, "world"},
		{"head range", "HEAD", "bytes=0-4", 206, 5, ""},
		{"invalid range", "GET", "bytes=999-", 416, -1, "invalid range: failed to overlap\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/assets/hello.txt", nil)
			req.Header.Set("Range", tc.rangeHeader)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.status || w.Body.String() != tc.body {
				t.Fatalf("response = %d %q", w.Code, w.Body.String())
			}
			if tc.length >= 0 && w.Result().ContentLength != int64(tc.length) {
				t.Fatalf("length = %d", w.Result().ContentLength)
			}
			if w.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatal("missing nosniff")
			}
			if tc.status == 206 && w.Header().Get("Content-Range") == "" {
				t.Fatal("missing Content-Range")
			}
		})
	}
	req := httptest.NewRequest("GET", "/assets/hello.txt", nil)
	req.Header.Set("If-Modified-Since", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotModified || w.Body.Len() != 0 {
		t.Fatalf("conditional response = %d %q", w.Code, w.Body.String())
	}
	conditional := gola.New()
	conditional.GET("/file", func(c *gola.Context) {
		c.Header("ETag", `"version-1"`)
		if err := c.File(store, "hello.txt"); err != nil {
			t.Error(err)
		}
	})
	req = httptest.NewRequest("GET", "/file", nil)
	req.Header.Set("If-None-Match", `"version-1"`)
	w = httptest.NewRecorder()
	conditional.ServeHTTP(w, req)
	if w.Code != 304 || w.Body.Len() != 0 {
		t.Fatalf("ETag response = %d %q", w.Code, w.Body.String())
	}
}

func TestFileStoreStaticRejectsHiddenDirectoriesAndSymlinks(t *testing.T) {
	store, dir := newTestFileStore(t)
	if err := os.Mkdir(filepath.Join(dir, "folder"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".env", "folder/index.html", "allowed.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("private sentinel"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "outside.txt"), []byte("outside sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths := []string{"", ".env", "%2eenv", "folder", "folder/", "../outside.txt", "%2e%2e/outside.txt", "%2Fetc/passwd", "missing", "a%5Cb"}
	for _, link := range []struct{ name, target string }{
		{"outside", outside}, {"outside-file", filepath.Join(outside, "outside.txt")},
		{"inside", "allowed.txt"}, {"hidden", ".env"}, {"directory-link", "folder"},
	} {
		if err := os.Symlink(link.target, filepath.Join(dir, link.name)); err != nil {
			if runtime.GOOS == "windows" {
				t.Logf("symlinks unavailable: %v", err)
				continue
			}
			t.Fatal(err)
		}
		paths = append(paths, link.name, link.name+"/outside.txt", link.name+"/index.html")
	}
	r := fileTestRouter(store)
	for _, name := range paths {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("GET", "/assets/"+name, nil))
			if w.Code != 404 || strings.Contains(w.Body.String(), "sentinel") || strings.Contains(w.Body.String(), dir) || w.Header().Get("Location") != "" {
				t.Fatalf("unsafe file response = %d %q %v", w.Code, w.Body.String(), w.Header())
			}
		})
	}
	file := storedUpload(t, "content")
	if err := store.Save(context.Background(), file, "outside/escaped.txt", 100); err == nil {
		t.Fatal("save followed parent symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, "escaped.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("file escaped root: %v", err)
	}
	if runtime.GOOS != "windows" {
		if err := store.Save(context.Background(), file, "inside", 100); !errors.Is(err, fs.ErrExist) {
			t.Fatalf("save overwrote symlink: %v", err)
		}
	}
}

func TestFileStoreAttachmentNamesAndCommitted(t *testing.T) {
	store, dir := newTestFileStore(t)
	if err := os.WriteFile(filepath.Join(dir, "data.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"report.txt", `my "report".txt`, "账单 2026.txt"} {
		t.Run(name, func(t *testing.T) {
			r := gola.New()
			r.GET("/", func(c *gola.Context) {
				if err := c.Attachment(store, "data.txt", name); err != nil {
					t.Error(err)
				}
			})
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
			media, params, err := mime.ParseMediaType(w.Header().Get("Content-Disposition"))
			if err != nil || media != "attachment" || params["filename"] != name || w.Body.String() != "hello" {
				t.Fatalf("attachment = %q, %q, %v", media, params, err)
			}
			if w.Header().Get("Content-Type") != "application/octet-stream" {
				t.Fatal("attachment content was sniffed")
			}
		})
	}
	for _, name := range []string{"", "\r\nX-Evil: yes", "../file", "a\\b", "\x00", ".", "..", "bad\xff", strings.Repeat("x", 256)} {
		t.Run("reject "+name, func(t *testing.T) {
			r := gola.New()
			r.GET("/", func(c *gola.Context) {
				if err := c.Attachment(store, "data.txt", name); !errors.Is(err, gola.ErrInvalidDownloadName) || c.Writer.Written() || len(c.Writer.Header()) != 0 {
					t.Errorf("invalid attachment changed response: %v, %v", err, c.Writer.Header())
				}
				c.Status(204)
			})
			r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
		})
	}
	r := gola.New()
	r.GET("/", func(c *gola.Context) {
		if err := c.File(store, "missing.txt"); !errors.Is(err, fs.ErrNotExist) || c.Writer.Written() {
			t.Errorf("missing File committed: %v", err)
		}
		c.Status(204)
		if err := c.File(store, "data.txt"); !errors.Is(err, gola.ErrResponseCommitted) {
			t.Error(err)
		}
		if err := c.Attachment(store, "data.txt", "bad\r\n"); !errors.Is(err, gola.ErrResponseCommitted) {
			t.Error(err)
		}
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 204 || w.Body.Len() != 0 || w.Header().Get("Content-Disposition") != "" {
		t.Fatalf("committed response changed: %d %q", w.Code, w.Body.String())
	}
}

func TestFileStoreMethodsAndClose(t *testing.T) {
	store, _ := newTestFileStore(t)
	r := gola.New()
	r.POST("/static/*file", store.Static("file"))
	r.POST("/file", func(c *gola.Context) {
		if err := c.File(store, "example.txt"); !errors.Is(err, gola.ErrFileMethod) || c.Writer.Written() {
			t.Errorf("File method error = %v", err)
		}
		c.Status(204)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/static/file", nil))
	if w.Code != 405 || w.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("static method = %d %v", w.Code, w.Header())
	}
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/file", nil))
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	var nilStore *gola.FileStore
	if err := nilStore.Save(context.Background(), storedUpload(t, "hello"), "hello.txt", 100); !errors.Is(err, gola.ErrFileStoreClosed) {
		t.Fatalf("nil store save = %v", err)
	}
	if err := nilStore.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), storedUpload(t, "hello"), "hello.txt", 100); !errors.Is(err, gola.ErrFileStoreClosed) {
		t.Fatalf("closed store save = %v", err)
	}
	w = httptest.NewRecorder()
	fileTestRouter(store).ServeHTTP(w, httptest.NewRequest("GET", "/assets/hello.txt", nil))
	if w.Code != 500 || w.Body.String() != "Internal Server Error\n" {
		t.Fatalf("closed store response = %d %q", w.Code, w.Body.String())
	}
}

func TestFileStoreUploadDownloadRoundTrip(t *testing.T) {
	store, dir := newTestFileStore(t)
	r := gola.New(gola.WithBodyLimit(1<<20), gola.WithMultipartMemory(0))
	r.POST("/upload", func(c *gola.Context) {
		file, err := c.FormFile("file")
		if err == nil {
			err = store.Save(c.Request.Context(), file, "server-selected.txt", 100)
		}
		if err != nil {
			c.Fail(err)
			return
		}
		c.Status(201)
	})
	r.GET("/download", func(c *gola.Context) {
		if err := c.Attachment(store, "server-selected.txt", "download.txt"); err != nil {
			c.Fail(err)
		}
	})
	server := httptest.NewServer(r)
	defer server.Close()
	body, media := makeMultipartPayload(t, nil, []multipartTestFile{{"file", "../../escaped.txt", "round-trip contents"}})
	response, err := server.Client().Post(server.URL+"/upload", media, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != 201 {
		t.Fatalf("upload status = %d", response.StatusCode)
	}
	response, err = server.Client().Get(server.URL + "/download")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != 200 || string(data) != "round-trip contents" {
		t.Fatalf("download = %d %q %v", response.StatusCode, data, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "server-selected.txt" {
		t.Fatalf("unexpected files: %v %v", entries, err)
	}
}
