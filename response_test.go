package gola_test

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	gola "github.com/starstack/gola"
)

func TestResponseCommitAndEncoding(t *testing.T) {
	e := gola.New()
	e.GET("/", func(c *gola.Context) {
		if err := c.JSON(201, make(chan int)); err == nil || c.Writer.Written() {
			t.Fatal("JSON encoding prematurely committed")
		}
		c.Header("X-Test", "yes")
		if err := c.JSON(202, gola.H{"ok": true}); err != nil {
			t.Fatal(err)
		}
		if c.Writer.Status() != 202 || c.Writer.Size() != 11 || !c.Writer.Written() {
			t.Fatal("writer state", c.Writer.Status(), c.Writer.Size())
		}
		for _, err := range []error{c.JSON(500, nil), c.String(500, "bad"), c.Data(500, "text/plain", nil), c.Redirect(302, "/x")} {
			if !errors.Is(err, gola.ErrResponseCommitted) {
				t.Error(err)
			}
		}
		c.Status(503)
		if _, err := c.Writer.Write([]byte("tail")); err != nil {
			t.Fatal(err)
		}
	})
	w := request(e, "GET", "/")
	if w.Code != 202 || w.Body.String() != `{"ok":true}tail` || w.Header().Get("X-Test") != "yes" {
		t.Fatal(w)
	}
}
func TestBodylessStatuses(t *testing.T) {
	for _, method := range []string{"GET", "HEAD"} {
		for _, status := range []int{200, 204, 304} {
			t.Run(fmt.Sprintf("%s/%d", method, status), func(t *testing.T) {
				e := gola.New()
				e.Handle(method, "/", func(c *gola.Context) {
					if err := c.String(status, "payload"); err != nil {
						t.Error(err)
					}
					if method == "HEAD" || status != 200 {
						if c.Writer.Size() != 0 {
							t.Error("body size", c.Writer.Size())
						}
					}
				})
				w := request(e, method, "/")
				if (method == "HEAD" || status != 200) && w.Body.Len() != 0 {
					t.Error(w.Body.String())
				}
			})
		}
	}
}

type trackedWriter struct {
	header http.Header
	codes  []int
	body   []byte
	err    error
}

func (w *trackedWriter) Header() http.Header { return w.header }
func (w *trackedWriter) WriteHeader(n int)   { w.codes = append(w.codes, n) }
func (w *trackedWriter) Write(b []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	w.body = append(w.body, b...)
	return len(b), nil
}
func TestInformationalResponseAndCapabilities(t *testing.T) {
	e := gola.New()
	e.GET("/", gola.WrapF(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := w.(http.Flusher); ok {
			t.Error("wrapper falsely advertises Flusher")
		}
		if _, ok := w.(http.Hijacker); ok {
			t.Error("wrapper falsely advertises Hijacker")
		}
		if err := http.NewResponseController(w).Flush(); !errors.Is(err, http.ErrNotSupported) {
			t.Fatal(err)
		}
		w.WriteHeader(103)
		w.WriteHeader(102)
		w.WriteHeader(201)
		w.WriteHeader(500)
		_, _ = w.Write([]byte("ok"))
	}))
	w := &trackedWriter{header: make(http.Header)}
	e.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if !reflect.DeepEqual(w.codes, []int{103, 102, 201}) || string(w.body) != "ok" {
		t.Fatal(w)
	}
}
func TestWriteErrorsPropagate(t *testing.T) {
	sentinel := errors.New("disconnected")
	e := gola.New()
	e.GET("/", func(c *gola.Context) {
		if err := c.Data(200, "text/plain", []byte("x")); !errors.Is(err, sentinel) {
			t.Fatal(err)
		}
		if c.Writer.Size() != 0 {
			t.Fatal(c.Writer.Size())
		}
	})
	e.ServeHTTP(&trackedWriter{header: make(http.Header), err: sentinel}, httptest.NewRequest("GET", "/", nil))
}
func TestErrorHandlingContract(t *testing.T) {
	secret := errors.New("database password: secret")
	e := gola.New()
	e.GET("/fail", func(c *gola.Context) { c.Fail(secret) }, func(*gola.Context) { t.Error("Fail did not abort") })
	e.GET("/record", func(c *gola.Context) {
		c.Error(secret)
		copy := c.Errors()
		copy[0] = io.EOF
		if c.Errors()[0] != secret {
			t.Error("errors expose internal storage")
		}
		_ = c.String(200, "ok")
	})
	e.GET("/committed", func(c *gola.Context) { _ = c.String(202, "done"); c.Fail(secret) })
	if w := request(e, "GET", "/fail"); w.Code != 500 || w.Body.String() != "Internal Server Error\n" {
		t.Fatal(w)
	}
	if w := request(e, "GET", "/record"); w.Code != 200 || w.Body.String() != "ok" {
		t.Fatal(w)
	}
	if w := request(e, "GET", "/committed"); w.Code != 202 || w.Body.String() != "done" {
		t.Fatal(w)
	}
	e2 := gola.New(gola.WithErrorHandler(func(c *gola.Context, err error) {
		if err != secret {
			t.Error(err)
		}
		_ = c.JSON(422, gola.H{"error": "invalid"})
	}))
	e2.GET("/", func(c *gola.Context) { c.Fail(secret) })
	if request(e2, "GET", "/").Code != 422 {
		t.Fatal("custom error handler")
	}
}
func TestRedirectValidationAndUntrustedPrefix(t *testing.T) {
	for _, target := range []string{"//evil.example", "/\\evil.example", "javascript:alert(1)", "/safe\r\nX-Injected: yes", "https://user:pass@example.com/", "relative", ""} {
		e := gola.New()
		e.GET("/", func(c *gola.Context) {
			if !errors.Is(c.Redirect(302, target), gola.ErrInvalidRedirect) || c.Writer.Written() {
				t.Errorf("unsafe target %q", target)
			}
			c.Status(204)
		})
		request(e, "GET", "/")
	}
	e := gola.New()
	e.GET("/", func(c *gola.Context) {
		if err := c.Redirect(302, "/safe"); err != nil {
			t.Error(err)
		}
	})
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Forwarded-Prefix", "//evil.example")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, r)
	if w.Header().Get("Location") != "/safe" {
		t.Fatal(w.Header())
	}
}
func TestInvalidOptions(t *testing.T) {
	for _, fn := range []func(){func() { gola.New(nil) }, func() { gola.New(gola.WithLogger(nil)) }, func() { gola.New(gola.WithBodyLimit(0)) }, func() { gola.New(gola.WithValidator(nil)) }, func() { gola.New(gola.WithErrorHandler(nil)) }, func() { gola.WrapH(nil) }, func() { gola.WrapF(nil) }} {
		mustPanic(t, fn)
	}
}
