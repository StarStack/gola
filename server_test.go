package gola_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/starstack/gola"
)

func awaitSignal(t *testing.T, signal <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestServerShutdownDrainsInFlightRequest(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	r := gola.New()
	r.GET("/slow", func(c *gola.Context) {
		close(entered)
		select {
		case <-release:
			if err := c.String(http.StatusOK, "completed"); err != nil {
				t.Errorf("write response: %v", err)
			}
		case <-c.Request.Context().Done():
			t.Error("graceful shutdown canceled an in-flight request")
		}
	})
	s := httptest.NewUnstartedServer(r)
	s.Start()
	defer s.Close()
	s.Client().Timeout = 3 * time.Second
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	responseDone := make(chan error, 1)
	go func() {
		resp, err := s.Client().Get(s.URL + "/slow")
		if err != nil {
			responseDone <- err
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err == nil && (resp.StatusCode != http.StatusOK || string(body) != "completed") {
			err = errors.New("in-flight response was truncated or changed")
		}
		responseDone <- err
	}()
	awaitSignal(t, entered, "handler entry")
	shutdownStarted := make(chan struct{})
	s.Config.RegisterOnShutdown(func() { close(shutdownStarted) })
	shutdownDone := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { shutdownDone <- s.Config.Shutdown(ctx) }()
	awaitSignal(t, shutdownStarted, "shutdown start")
	select {
	case err := <-shutdownDone:
		t.Fatalf("Shutdown returned before in-flight request finished: %v", err)
	default:
	}
	close(release)
	if err := <-responseDone; err != nil {
		t.Fatal(err)
	}
	if err := <-shutdownDone; err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func TestServerStartupAndShutdownErrors(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := gola.New().Run(listener.Addr().String()); err == nil || errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("occupied port must produce a startup error: %v", err)
	}
	entered, canceled := make(chan struct{}), make(chan struct{})
	r := gola.New()
	r.GET("/wait", func(c *gola.Context) {
		close(entered)
		<-c.Request.Context().Done()
		close(canceled)
	})
	s := httptest.NewServer(r)
	defer s.Close()
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		resp, err := s.Client().Get(s.URL + "/wait")
		if err == nil {
			resp.Body.Close()
		}
	}()
	awaitSignal(t, entered, "handler entry")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := s.Config.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown deadline error = %v", err)
	}
	if err := s.Config.Close(); err != nil {
		t.Fatal(err)
	}
	awaitSignal(t, canceled, "request cancellation after forced close")
	awaitSignal(t, clientDone, "client completion")
}

func TestResponseControllerFlushAndClientDisconnect(t *testing.T) {
	finished := make(chan struct{})
	errorsFromHandler := make(chan error, 1)
	var writes atomic.Int32
	r := gola.New()
	r.GET("/stream", gola.WrapF(func(w http.ResponseWriter, req *http.Request) {
		defer close(finished)
		w.Header().Set("Content-Type", "text/plain")
		if _, err := w.Write([]byte("first\n")); err != nil {
			errorsFromHandler <- err
			return
		}
		writes.Add(1)
		if err := http.NewResponseController(w).Flush(); err != nil {
			errorsFromHandler <- err
			return
		}
		<-req.Context().Done()
	}))
	s := httptest.NewServer(r)
	defer s.Close()
	resp, err := s.Client().Get(s.URL + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	first := make([]byte, len("first\n"))
	if _, err := io.ReadFull(resp.Body, first); err != nil {
		t.Fatal(err)
	}
	if string(first) != "first\n" {
		t.Fatalf("flushed response = %q", first)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	awaitSignal(t, finished, "handler cancellation after client disconnect")
	if got := writes.Load(); got != 1 {
		t.Fatalf("continued writing after disconnect: %d", got)
	}
	select {
	case err := <-errorsFromHandler:
		t.Fatalf("standard response capability failed: %v", err)
	default:
	}
}

type failingResponseWriter struct {
	header http.Header
	err    error
}

func (w *failingResponseWriter) Header() http.Header       { return w.header }
func (w *failingResponseWriter) WriteHeader(int)           {}
func (w *failingResponseWriter) Write([]byte) (int, error) { return 0, w.err }

func TestResponseWriteErrorPropagatesAndUnsupportedCapability(t *testing.T) {
	want := errors.New("connection closed")
	r := gola.New()
	r.GET("/", func(c *gola.Context) {
		if err := http.NewResponseController(c.Writer).Flush(); !errors.Is(err, http.ErrNotSupported) {
			t.Errorf("unsupported Flush should return ErrNotSupported, got %v", err)
		}
		if err := c.String(http.StatusOK, "hello"); !errors.Is(err, want) {
			t.Errorf("write error = %v, want %v", err, want)
		}
	})
	r.ServeHTTP(&failingResponseWriter{header: make(http.Header), err: want}, httptest.NewRequest(http.MethodGet, "/", strings.NewReader("")))
}

func TestStandardHandlerHijackPreservesUpgrade(t *testing.T) {
	var serverLog bytes.Buffer
	finished := make(chan struct{})
	r := gola.New()
	r.GET("/upgrade", gola.WrapF(func(w http.ResponseWriter, req *http.Request) {
		conn, rw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Errorf("Hijack: %v", err)
			return
		}
		defer conn.Close()
		if _, err := rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: gola-test\r\n\r\nupgraded"); err != nil {
			t.Errorf("upgrade write: %v", err)
			return
		}
		if err := rw.Flush(); err != nil {
			t.Errorf("upgrade flush: %v", err)
		}
	}))
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		defer close(finished)
		r.ServeHTTP(w, req)
	}))
	s.Config.ErrorLog = log.New(&serverLog, "", 0)
	s.Start()
	defer s.Close()
	conn, err := net.DialTimeout("tcp", s.Listener.Addr().String(), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, "GET /upgrade HTTP/1.1\r\nHost: localhost\r\nConnection: Upgrade\r\nUpgrade: gola-test\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	wire, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	awaitSignal(t, finished, "upgraded handler completion")
	if !strings.HasPrefix(string(wire), "HTTP/1.1 101 Switching Protocols\r\n") || !strings.HasSuffix(string(wire), "\r\n\r\nupgraded") {
		t.Fatalf("upgrade wire response = %q", wire)
	}
	if serverLog.Len() != 0 {
		t.Fatalf("framework wrote to hijacked connection: %s", serverLog.String())
	}
}
