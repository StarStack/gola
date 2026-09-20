package gola

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestDefaultRequestIDAndAccessLog(t *testing.T) {
	var logs bytes.Buffer
	e := Default(WithLogger(slog.New(slog.NewJSONHandler(&logs, nil))))
	e.GET("/ok", func(c *Context) { _ = c.String(201, "ok") })
	ids := map[string]bool{}
	for _, tt := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/ok", 201}, {"GET", "/absent%0Aforged?password=secret", 404}, {"POST", "/ok", 405},
	} {
		r := httptest.NewRequest(tt.method, tt.path, nil)
		r.Header.Set("X-Request-ID", "untrusted-id")
		r.Header.Set("Authorization", "Bearer credential-secret")
		r.Header.Set("Cookie", "private-cookie")
		w := httptest.NewRecorder()
		e.ServeHTTP(w, r)
		id := w.Header().Get("X-Request-ID")
		if decoded, err := hex.DecodeString(id); err != nil || len(decoded) != 16 || ids[id] {
			t.Fatalf("bad or reused ID %q", id)
		}
		ids[id] = true
		if w.Code != tt.status {
			t.Fatalf("status=%d want=%d", w.Code, tt.status)
		}
	}
	if strings.Contains(logs.String(), "secret") || strings.Contains(logs.String(), "private-cookie") || strings.Contains(logs.String(), "untrusted-id") {
		t.Fatal("logs contain secrets")
	}
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 log entries: %s", logs.String())
	}
	for i, line := range lines {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"time", "request_id", "method", "route", "status", "duration", "response_bytes"} {
			if _, ok := entry[field]; !ok {
				t.Errorf("missing %s", field)
			}
		}
		if i == 1 && strings.Contains(entry["path"].(string), "\n") {
			t.Fatal("path contains unescaped newline")
		}
	}
}

func TestAccessLogPathBounded(t *testing.T) {
	var logs bytes.Buffer
	e := Default(WithLogger(slog.New(slog.NewJSONHandler(&logs, nil))))
	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/"+strings.Repeat("x", 10000), nil))
	if logs.Len() > 2000 {
		t.Fatalf("unbounded unmatched path log: %d bytes", logs.Len())
	}
}

func TestRequestIDConcurrentIsolation(t *testing.T) {
	e := New()
	e.Use(RequestID())
	e.GET("/", func(c *Context) { id, _ := c.Get(RequestIDKey); _ = c.String(200, "%s", id) })
	var wg sync.WaitGroup
	results := make(chan string, 64)
	for range 64 {
		wg.Go(func() {
			w := httptest.NewRecorder()
			e.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
			if w.Body.String() != w.Header().Get("X-Request-ID") {
				t.Error("request id leaked between contexts")
			}
			results <- w.Body.String()
		})
	}
	wg.Wait()
	close(results)
	seen := map[string]bool{}
	for id := range results {
		if seen[id] {
			t.Fatal("duplicate concurrent request ID")
		}
		seen[id] = true
	}
}

func TestRecoveryBeforeAndAfterCommit(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "after"}[committed], func(t *testing.T) {
			var logs bytes.Buffer
			e := Default(WithLogger(slog.New(slog.NewJSONHandler(&logs, nil))))
			next := false
			e.GET("/", func(c *Context) {
				if committed {
					_ = c.String(202, "partial")
				}
				panic("secret panic details")
			}, func(*Context) { next = true })
			w := httptest.NewRecorder()
			var caught any
			func() { defer func() { caught = recover() }(); e.ServeHTTP(w, httptest.NewRequest("GET", "/", nil)) }()
			if next {
				t.Fatal("panic continued the chain")
			}
			if committed {
				if caught != http.ErrAbortHandler || w.Code != 202 || w.Body.String() != "partial" {
					t.Fatalf("committed panic: recovered=%v status=%d body=%q", caught, w.Code, w.Body.String())
				}
			} else if caught != nil || w.Code != 500 || w.Body.String() != "Internal Server Error\n" {
				t.Fatalf("uncommitted panic: recovered=%v status=%d body=%q", caught, w.Code, w.Body.String())
			}
			if strings.Contains(logs.String(), "secret panic details") {
				t.Fatal("panic values leaked to logs")
			}
			lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
			if len(lines) != 2 {
				t.Fatalf("expected panic and access log: %s", logs.String())
			}
			var access map[string]any
			if err := json.Unmarshal([]byte(lines[1]), &access); err != nil {
				t.Fatal(err)
			}
			if access["status"] != float64(w.Code) || access["aborted_response"] != committed {
				t.Fatalf("incorrect final access log: %v", access)
			}
		})
	}
}

func TestRecoveryPreservesAbortHandler(t *testing.T) {
	var logs bytes.Buffer
	e := Default(WithLogger(slog.New(slog.NewJSONHandler(&logs, nil))))
	e.GET("/", func(*Context) { panic(http.ErrAbortHandler) })
	w := httptest.NewRecorder()
	defer func() {
		if recover() != http.ErrAbortHandler {
			t.Error("ErrAbortHandler was swallowed")
		}
		if w.Body.Len() != 0 {
			t.Error("abort sentinel generated response body")
		}
		if strings.Count(logs.String(), "http request") != 1 {
			t.Error("missing deferred access log")
		}
	}()
	e.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
}

func TestRecoveryTruncationOverHTTP(t *testing.T) {
	e := Default(WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	e.GET("/", func(c *Context) {
		_, _ = c.Writer.Write([]byte("partial"))
		if err := http.NewResponseController(c.Writer).Flush(); err != nil {
			panic(err)
		}
		panic("after-flush")
	})
	s := httptest.NewServer(e)
	defer s.Close()
	r, err := s.Client().Get(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err == nil || string(body) != "partial" {
		t.Fatalf("truncation was reported as successful: body=%q err=%v", body, err)
	}
}

func TestBodyLimitActualReads(t *testing.T) {
	for _, known := range []bool{false, true} {
		e := New()
		e.Use(BodyLimit(4))
		called := false
		e.POST("/", func(c *Context) {
			called = true
			_, err := io.ReadAll(c.Request.Body)
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				c.AbortWithStatus(413)
				return
			}
			if err != nil {
				t.Error(err)
			}
			c.Status(204)
		})
		r := httptest.NewRequest("POST", "/", strings.NewReader("12345"))
		if !known {
			r.ContentLength = -1
			r.TransferEncoding = []string{"chunked"}
		}
		w := httptest.NewRecorder()
		e.ServeHTTP(w, r)
		if w.Code != 413 || called == known {
			t.Fatalf("known=%v called=%v status=%d", known, called, w.Code)
		}
	}
}
