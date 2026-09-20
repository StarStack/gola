package gola_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/starstack/gola"
)

func TestSSEFramesAndInjection(t *testing.T) {
	e := gola.New()
	e.GET("/", func(c *gola.Context) {
		c.Header("Content-Length", "1")
		for _, name := range []string{"bad\nevent: injected", "bad\rname", "bad\x00name", string([]byte{0xff})} {
			if err := c.SSEvent(name, "value"); !errors.Is(err, gola.ErrInvalidSSEEvent) || c.Writer.Written() {
				t.Fatal("invalid event was committed", err)
			}
		}
		if err := c.SSEvent("ready", make(chan int)); err == nil || c.Writer.Written() {
			t.Fatal("invalid data was committed")
		}
		if err := c.SSEvent("ready", "first\r\n\nevent: injected\ndata: second"); err != nil {
			t.Fatal(err)
		}
		if err := c.SSEvent("", gola.H{"count": 2}); err != nil {
			t.Fatal(err)
		}
		if err := c.JSON(200, "too late"); !errors.Is(err, gola.ErrResponseCommitted) {
			t.Fatal(err)
		}
	})
	w := request(e, "GET", "/")
	want := "event: ready\ndata: \"first\\r\\n\\nevent: injected\\ndata: second\"\n\ndata: {\"count\":2}\n\n"
	if w.Code != 200 || w.Body.String() != want || !w.Flushed || w.Header().Get("Content-Type") != "text/event-stream; charset=utf-8" || w.Header().Get("Cache-Control") != "no-cache" || w.Header().Get("Content-Length") != "" {
		t.Fatalf("unexpected SSE response: %d %v %q", w.Code, w.Header(), w.Body.String())
	}
}

func TestSSECommitAndCancellation(t *testing.T) {
	e := gola.New()
	e.GET("/committed", func(c *gola.Context) {
		_ = c.String(200, "ordinary response")
		if err := c.SSEvent("ready", nil); !errors.Is(err, gola.ErrResponseCommitted) {
			t.Fatal(err)
		}
	})
	e.GET("/changed", func(c *gola.Context) {
		_ = c.SSEvent("ready", nil)
		c.Header("Content-Type", "application/json")
		if err := c.SSEvent("next", nil); !errors.Is(err, gola.ErrResponseCommitted) {
			t.Fatal(err)
		}
	})
	e.GET("/cancelled", func(c *gola.Context) {
		if err := c.SSEvent("ready", nil); !errors.Is(err, context.Canceled) || c.Writer.Written() {
			t.Fatal(err)
		}
	})
	e.HEAD("/", func(c *gola.Context) {
		if err := c.SSEvent("ready", nil); !errors.Is(err, http.ErrBodyNotAllowed) || c.Writer.Written() {
			t.Fatal(err)
		}
	})
	request(e, "GET", "/committed")
	request(e, "GET", "/changed")
	request(e, "HEAD", "/")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/cancelled", nil).WithContext(ctx))
}

func TestSSEPreservesApplicationCachePolicy(t *testing.T) {
	r := gola.New()
	r.GET("/", func(c *gola.Context) {
		c.Header("Cache-Control", "private, no-store")
		if err := c.SSEvent("balance", 42); err != nil {
			t.Fatal(err)
		}
	})
	w := request(r, "GET", "/")
	if got := w.Result().Header.Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("application cache policy overwritten: %q", got)
	}
}

type sseUnwrapper struct{ http.ResponseWriter }

func (w sseUnwrapper) Unwrap() http.ResponseWriter { return w.ResponseWriter }

type sseFlushFailure struct {
	*trackedWriter
	flushErr error
}

func (w *sseFlushFailure) FlushError() error { return w.flushErr }

func TestSSEFlushCapabilitiesAndErrors(t *testing.T) {
	e := gola.New()
	e.GET("/", func(c *gola.Context) {
		if err := c.SSEvent("ready", nil); !errors.Is(err, http.ErrNotSupported) || c.Writer.Written() || c.Writer.Header().Get("Content-Type") != "" {
			t.Fatal("unsupported transport committed headers", err)
		}
		_ = c.String(503, "streaming unavailable")
	})
	w := &trackedWriter{header: make(http.Header)}
	e.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if string(w.body) != "streaming unavailable" {
		t.Fatal(string(w.body))
	}
	for _, writeFailure := range []bool{false, true} {
		sentinel := errors.New("transport stopped")
		r := gola.New()
		r.GET("/", func(c *gola.Context) {
			if err := c.SSEvent("ready", nil); !errors.Is(err, sentinel) {
				t.Fatal(err)
			}
		})
		writer := &sseFlushFailure{trackedWriter: &trackedWriter{header: make(http.Header)}, flushErr: sentinel}
		if writeFailure {
			writer.err = sentinel
		}
		r.ServeHTTP(sseUnwrapper{writer}, httptest.NewRequest("GET", "/", nil))
	}
}

func TestSSERealClientDisconnectCancelsHandler(t *testing.T) {
	e := gola.New()
	finished := make(chan error, 1)
	e.GET("/events", func(c *gola.Context) {
		if err := c.SSEvent("ready", true); err != nil {
			finished <- err
			return
		}
		<-c.Request.Context().Done()
		finished <- c.Request.Context().Err()
	})
	server := httptest.NewServer(e)
	defer server.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get(server.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(response.Body).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "event: ready" {
		response.Body.Close()
		t.Fatal("initial event was not flushed", line, err)
	}
	response.Body.Close()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not stop after disconnect")
	}
}

func FuzzSSEFraming(f *testing.F) {
	f.Add("ready", "value")
	f.Add("ready", "\r\n\nevent: injected\ndata: another")
	f.Add("bad\nname", "value")
	f.Add(string([]byte{0xff}), "value")
	f.Fuzz(func(t *testing.T, name, data string) {
		e := gola.New()
		var sendErr error
		e.GET("/", func(c *gola.Context) {
			sendErr = c.SSEvent(name, data)
			if sendErr != nil && c.Writer.Written() {
				t.Fatal("invalid event was committed")
			}
		})
		w := request(e, "GET", "/")
		if sendErr != nil {
			return
		}
		frame := w.Body.String()
		if strings.Count(frame, "\n\n") != 1 || strings.ContainsRune(frame, '\r') {
			t.Fatal("injected event delimiter", frame)
		}
		payload := strings.Split(strings.TrimSuffix(frame, "\n\n"), "\n")
		if len(payload) > 2 {
			t.Fatal("injected SSE field", frame)
		}
		var decoded string
		if err := json.Unmarshal([]byte(strings.TrimPrefix(payload[len(payload)-1], "data: ")), &decoded); err != nil {
			t.Fatal("event data is not JSON", err)
		}
	})
}
