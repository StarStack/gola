package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/starstack/gola/middleware/compress"
)

func startTestServer(t *testing.T, handler *socketHandler, compressed bool) *httptest.Server {
	t.Helper()
	var router http.Handler = newRouter(handler)
	if compressed {
		middleware, err := compress.New(compress.Config{})
		if err != nil {
			t.Fatal(err)
		}
		router = middleware(router)
	}
	server := httptest.NewServer(router)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = handler.Shutdown(ctx)
		server.Close()
	})
	return server
}

func dialTestSocket(t *testing.T, server *httptest.Server, origin string) *websocket.Conn {
	t.Helper()
	headers := make(http.Header)
	if origin != "" {
		headers.Set("Origin", origin)
	}
	headers.Set("Accept-Encoding", "gzip")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, response, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/ws", &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusSwitchingProtocols || response.Header.Get("Content-Encoding") != "" {
		conn.CloseNow()
		t.Fatal("upgrade was encoded or replaced", response.StatusCode, response.Header)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

func TestWebSocketRoundTripAndCompressionWrapper(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		name := "plain"
		if compressed {
			name = "compression wrapper"
		}
		t.Run(name, func(t *testing.T) {
			handler := newSocketHandler(nil)
			server := startTestServer(t, handler, compressed)
			conn := dialTestSocket(t, server, server.URL)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			for _, message := range []struct {
				kind websocket.MessageType
				data []byte
			}{{websocket.MessageText, []byte("Hello Danny")}, {websocket.MessageBinary, []byte{0, 1, 255}}, {websocket.MessageText, []byte("second message")}} {
				if err := conn.Write(ctx, message.kind, message.data); err != nil {
					t.Fatal(err)
				}
				kind, data, err := conn.Read(ctx)
				if err != nil || kind != message.kind || !bytes.Equal(data, message.data) {
					t.Fatal("round trip failed", kind, string(data), err)
				}
			}
			if err := conn.Close(websocket.StatusNormalClosure, ""); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWebSocketRejectsCrossOrigin(t *testing.T) {
	handler := newSocketHandler(nil)
	server := startTestServer(t, handler, false)
	for _, origin := range []string{"https://untrusted.example", "null", server.URL + "/path", strings.Replace(server.URL, "http://", "https://", 1), ""} {
		headers := make(http.Header)
		headers.Set("Origin", origin)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		conn, response, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/ws", &websocket.DialOptions{HTTPHeader: headers})
		cancel()
		if conn != nil {
			conn.CloseNow()
		}
		if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
			t.Fatalf("origin %q accepted: %v", origin, err)
		}
	}
	// A native client can omit Origin; the endpoint still needs independent
	// application authentication before carrying privileged data.
	conn := dialTestSocket(t, server, "")
	_ = conn.CloseNow()
}

func TestSameOriginRejectsMalformedAndDuplicateHeaders(t *testing.T) {
	for _, origins := range [][]string{
		{"http://example.com", "http://example.com"}, {"http://user@example.com"},
		{"http://example.com?x=1"}, {"http://example.com#fragment"}, {"http://example.com:81"},
		{"http://example.com?"}, {"http://example.com#"},
		{"http://example.com http://untrusted.example"}, {"//example.com"},
	} {
		req := httptest.NewRequest("GET", "http://example.com/ws", nil)
		for _, origin := range origins {
			req.Header.Add("Origin", origin)
		}
		if sameOrigin(req) {
			t.Fatal("accepted invalid origin", origins)
		}
	}
	req := httptest.NewRequest("GET", "https://example.com/ws", nil)
	req.TLS = &tls.ConnectionState{}
	req.Header.Set("Origin", "https://example.com")
	if !sameOrigin(req) {
		t.Fatal("rejected same HTTPS origin")
	}
}

func TestWebSocketMessageLimit(t *testing.T) {
	server := startTestServer(t, newSocketHandler(nil), false)
	conn := dialTestSocket(t, server, server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, bytes.Repeat([]byte("x"), maxMessageBytes+1)); err != nil {
		t.Fatal(err)
	}
	_, _, err := conn.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusMessageTooBig {
		t.Fatal("oversized message did not close with 1009", err)
	}
}

func TestWebSocketPanicDoesNotExposeValue(t *testing.T) {
	secret := "private application credential"
	handler := newSocketHandler(func([]byte) []byte { panic(secret) })
	server := startTestServer(t, handler, false)
	conn := dialTestSocket(t, server, server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, []byte("trigger")); err != nil {
		t.Fatal(err)
	}
	_, data, err := conn.Read(ctx)
	var closeErr websocket.CloseError
	if !errors.As(err, &closeErr) || closeErr.Code != websocket.StatusInternalError || closeErr.Reason != "internal error" || len(data) != 0 || strings.Contains(err.Error(), secret) {
		t.Fatal("panic response was not generic", string(data), err)
	}
}

func TestWebSocketServerShutdownClosesUpgradedConnections(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handler := newSocketHandler(nil)
	finished := make(chan error, 1)
	go func() { finished <- serve(ctx, listener, handler) }()
	dialContext, cancelDial := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelDial()
	conn, _, err := websocket.Dial(dialContext, "ws://"+listener.Addr().String()+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	cancel()
	_, _, err = conn.Read(dialContext)
	if websocket.CloseStatus(err) != websocket.StatusGoingAway {
		t.Fatal("shutdown did not send going away", err)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not join WebSocket handlers")
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if !handler.closing || handler.count != 0 || len(handler.clients) != 0 {
		t.Fatal("shutdown retained active connections")
	}
}

func TestWebSocketShutdownBoundsUnresponsiveClient(t *testing.T) {
	handler := newSocketHandler(nil)
	server := startTestServer(t, handler, false)
	conn := dialTestSocket(t, server, server.URL)
	defer conn.CloseNow()
	// Ensure the handler has completed registration by reading an echo first.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, []byte("ready")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := conn.Read(ctx); err != nil {
		t.Fatal(err)
	}
	shutdown, stop := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer stop()
	start := time.Now()
	err := handler.Shutdown(shutdown) // The client no longer reads close frames.
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatal("shutdown did not force bounded cleanup", err, time.Since(start))
	}
}
