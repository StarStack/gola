package compress

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func wrap(t *testing.T, next http.HandlerFunc) http.Handler {
	t.Helper()
	mw, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	return mw(next)
}

func decode(t *testing.T, data []byte) []byte {
	t.Helper()
	z, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	p, err := io.ReadAll(z)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEncodingNegotiation(t *testing.T) {
	for _, tt := range []struct {
		values []string
		gzip   bool
	}{
		{nil, false}, {[]string{""}, false}, {[]string{"br"}, false},
		{[]string{"gzip"}, true}, {[]string{"GZIP;Q=1.000"}, true},
		{[]string{"br, gzip;q=0.5"}, true}, {[]string{"gzip;q=0"}, false},
		{[]string{"gzip;q=0", "*;q=1"}, false}, {[]string{"*;q=0.5"}, true},
		{[]string{"*;q=0", "gzip"}, true}, {[]string{"gzip", "gzip;q=0"}, false},
		{[]string{"gzip;q=NaN"}, false}, {[]string{"gzip;q=1.001"}, false},
		{[]string{"gzip;q=-1"}, false}, {[]string{"gzip;q=.5"}, false},
		{[]string{"gzip;q=0.1111"}, false}, {[]string{"gzip;q=1;q=0"}, false},
		{[]string{"gzip;bogus=1"}, false}, {[]string{"gzip;q=0."}, false},
		{[]string{"gzip;q=1."}, true},
	} {
		t.Run(strings.Join(tt.values, "|"), func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.Header["Accept-Encoding"] = tt.values
			w := httptest.NewRecorder()
			wrap(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "hello") }).ServeHTTP(w, r)
			if got := w.Header().Get("Content-Encoding") == "gzip"; got != tt.gzip {
				t.Fatalf("gzip=%v, want %v", got, tt.gzip)
			}
			body := w.Body.Bytes()
			if tt.gzip {
				body = decode(t, body)
			}
			if string(body) != "hello" || !hasToken(w.Header().Values("Vary"), "Accept-Encoding") {
				t.Fatalf("invalid body or Vary: %s %v", body, w.Header())
			}
		})
	}
}

func TestResponseHeadersAndStreaming(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	body := strings.Repeat("streaming body ", 10_000)
	wrap(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", "150000")
		w.Header().Set("Content-MD5", "old")
		w.Header().Set("Content-Digest", "old")
		w.Header().Set("ETag", `"identity"`)
		w.Header().Set("Vary", "Origin, accept-encoding")
		if _, err := io.WriteString(w, body[:1024]); err != nil {
			t.Fatal(err)
		}
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Fatal(err)
		}
		if !w.(*writer).underlying.(*httptest.ResponseRecorder).Flushed {
			t.Fatal("transport was not flushed before handler finished")
		}
		if _, err := io.WriteString(w, body[1024:]); err != nil {
			t.Fatal(err)
		}
	}).ServeHTTP(w, r)
	if got := string(decode(t, w.Body.Bytes())); got != body {
		t.Fatal("stream changed")
	}
	if w.Header().Get("Content-Length") != "" || w.Header().Get("Content-MD5") != "" || w.Header().Get("Content-Digest") != "" || w.Header().Get("ETag") != `W/"identity"` || len(w.Header().Values("Vary")) != 1 {
		t.Fatalf("headers=%v", w.Header())
	}
}

func TestIdentityVaryAfterHandlerChanges(t *testing.T) {
	w := httptest.NewRecorder()
	wrap(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Vary", "Origin")
		_, _ = io.WriteString(w, "identity")
	}).ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if !hasToken(w.Header().Values("Vary"), "Accept-Encoding") || !hasToken(w.Header().Values("Vary"), "Origin") {
		t.Fatalf("Vary lost: %v", w.Header())
	}
}

func TestHTTPIntegration(t *testing.T) {
	server := httptest.NewServer(wrap(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", "11")
		_, _ = io.WriteString(w, "hello world")
	}))
	defer server.Close()
	for _, method := range []string{"GET", "HEAD"} {
		r, err := http.NewRequest(method, server.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Accept-Encoding", "gzip")
		res, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if method == "GET" {
			if res.Header.Get("Content-Encoding") != "gzip" || string(decode(t, data)) != "hello world" {
				t.Fatalf("invalid GET: %v %q", res.Header, data)
			}
		} else if len(data) != 0 || res.Header.Get("Content-Encoding") != "" || res.ContentLength != 11 {
			t.Fatalf("invalid HEAD: %v %q", res.Header, data)
		}
	}
}

func TestUnwrapPreservesControllerCapabilities(t *testing.T) {
	underlying := &deadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	wrap(t, func(w http.ResponseWriter, r *http.Request) {
		if err := http.NewResponseController(w).EnableFullDuplex(); err != nil {
			t.Fatal(err)
		}
	}).ServeHTTP(underlying, httptest.NewRequest("GET", "/", nil))
	if !underlying.fullDuplex {
		t.Fatal("Unwrap lost controller access")
	}
}

type deadlineWriter struct {
	*httptest.ResponseRecorder
	fullDuplex bool
}

func (w *deadlineWriter) EnableFullDuplex() error { w.fullDuplex = true; return nil }

func TestCompressionTrailer(t *testing.T) {
	// Unrelated application trailers survive compression and ResponseController.
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	wrap(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Trailer", "X-Result")
		_, _ = io.WriteString(w, "hello")
		w.Header().Set("X-Result", "complete")
	}).ServeHTTP(w, r)
	if string(decode(t, w.Body.Bytes())) != "hello" || w.Result().Trailer.Get("X-Result") != "complete" {
		t.Fatal("response trailer lost")
	}
}

func TestCompressionExclusions(t *testing.T) {
	for _, tt := range []struct {
		name, method, reqHeader, reqValue, respHeader, respValue string
		status                                                   int
	}{
		{"head", "HEAD", "", "", "", "", 200},
		{"range", "GET", "Range", "bytes=0-3", "", "", 200},
		{"upgrade", "GET", "Upgrade", "websocket", "", "", 101},
		{"connection upgrade", "GET", "Connection", "keep-alive, Upgrade", "", "", 200},
		{"connect", "CONNECT", "", "", "", "", 200},
		{"no content", "GET", "", "", "", "", 204},
		{"reset content", "GET", "", "", "", "", 205},
		{"not modified", "GET", "", "", "", "", 304},
		{"partial", "GET", "", "", "", "", 206},
		{"content range", "GET", "", "", "Content-Range", "bytes 0-3/100", 200},
		{"already compressed", "GET", "", "", "Content-Encoding", "br", 200},
		{"sse", "GET", "", "", "Content-Type", "text/event-stream; charset=utf-8", 200},
		{"no transform", "GET", "", "", "Cache-Control", "private, no-transform", 200},
		{"digest trailer", "GET", "", "", "Trailer", "X-Result, Content-Digest", 200},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(tt.method, "http://example.com/", nil)
			r.Header.Set("Accept-Encoding", "gzip")
			if tt.reqHeader != "" {
				r.Header.Set(tt.reqHeader, tt.reqValue)
			}
			w := httptest.NewRecorder()
			wrap(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/plain")
				if tt.respHeader != "" {
					w.Header().Set(tt.respHeader, tt.respValue)
				}
				w.WriteHeader(tt.status)
				if tt.status == 200 && tt.method != "HEAD" {
					_, _ = io.WriteString(w, "body")
				}
			}).ServeHTTP(w, r)
			if w.Header().Get("Content-Encoding") == "gzip" || w.Code != tt.status {
				t.Fatalf("unexpected encoding/status: %v %d", w.Header(), w.Code)
			}
		})
	}
}

type headersRecorder struct {
	*httptest.ResponseRecorder
	statuses []int
}

func (w *headersRecorder) WriteHeader(status int) {
	w.statuses = append(w.statuses, status)
	if status >= 200 || status == 101 {
		w.ResponseRecorder.WriteHeader(status)
	}
}

func TestInterimResponseAndFirstFinalStatus(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	w := &headersRecorder{ResponseRecorder: httptest.NewRecorder()}
	wrap(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(103)
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(201)
		w.WriteHeader(500)
		_, _ = io.WriteString(w, "created")
	}).ServeHTTP(w, r)
	if len(w.statuses) != 2 || w.statuses[0] != 103 || w.statuses[1] != 201 || string(decode(t, w.Body.Bytes())) != "created" {
		t.Fatalf("statuses=%v body=%q", w.statuses, w.Body.Bytes())
	}
}

func TestExplicitStatusWithoutContentTypePreservesSniffing(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	wrap(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "plain")
	}).ServeHTTP(w, r)
	if w.Header().Get("Content-Encoding") != "" || w.Body.String() != "plain" {
		t.Fatalf("unexpected transformation: %v %q", w.Header(), w.Body.String())
	}
}

func TestPanicDoesNotFinishGzip(t *testing.T) {
	for _, panicValue := range []any{"original", http.ErrAbortHandler} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Accept-Encoding", "gzip")
		w := httptest.NewRecorder()
		func() {
			defer func() {
				if got := recover(); got != panicValue {
					t.Fatalf("panic changed: %v", got)
				}
			}()
			wrap(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, "partial")
				_ = http.NewResponseController(w).Flush()
				panic(panicValue)
			}).ServeHTTP(w, r)
		}()
		z, err := gzip.NewReader(bytes.NewReader(w.Body.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.ReadAll(z)
		_ = z.Close()
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("panicking response was finished: %v", err)
		}
	}
}

type noFlushWriter struct{ h http.Header }

func (w noFlushWriter) Header() http.Header       { return w.h }
func (noFlushWriter) WriteHeader(int)             {}
func (noFlushWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestUnsupportedControllerAndInvalidLevel(t *testing.T) {
	for _, level := range []int{-3, 10} {
		if _, err := New(Config{Level: level}); err == nil {
			t.Fatal("invalid level accepted")
		}
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	wrap(t, func(w http.ResponseWriter, r *http.Request) {
		if err := http.NewResponseController(w).Flush(); !errors.Is(err, http.ErrNotSupported) {
			t.Fatalf("flush error=%v", err)
		}
	}).ServeHTTP(noFlushWriter{make(http.Header)}, r)
}

type failedBodyWriter struct{ h http.Header }

func (w failedBodyWriter) Header() http.Header { return w.h }
func (failedBodyWriter) WriteHeader(int)       {}
func (failedBodyWriter) Write([]byte) (int, error) {
	return 0, io.ErrClosedPipe
}

func TestFinalCompressionWriteFailureAborts(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	defer func() {
		if got := recover(); got != http.ErrAbortHandler {
			t.Fatalf("expected transport abort, got %v", got)
		}
	}()
	wrap(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(200)
	}).ServeHTTP(failedBodyWriter{make(http.Header)}, r)
}

func FuzzAcceptEncoding(f *testing.F) {
	for _, value := range []string{"gzip", "*;q=0", "gzip;q=0, *;q=1", "gzip;q=NaN", "GZIP;q=0.5"} {
		f.Add(value)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if len(value) > 8192 {
			t.Skip()
		}
		r := httptest.NewRequest("GET", "/", nil)
		r.Header["Accept-Encoding"] = []string{value}
		w := httptest.NewRecorder()
		wrap(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "payload") }).ServeHTTP(w, r)
		data := w.Body.Bytes()
		if w.Header().Get("Content-Encoding") == "gzip" {
			data = decode(t, data)
		}
		if string(data) != "payload" {
			t.Fatal("payload changed")
		}
	})
}
