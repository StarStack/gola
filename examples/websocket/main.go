package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/starstack/gola"
)

const (
	maxMessageBytes = 64 << 10
	maxConnections  = 128
	readTimeout     = 60 * time.Second
	writeTimeout    = 5 * time.Second
)

type socketHandler struct {
	mu      sync.Mutex
	closing bool
	count   int
	active  sync.WaitGroup
	clients map[*websocket.Conn]context.CancelFunc
	process func([]byte) []byte
}

func newSocketHandler(process func([]byte) []byte) *socketHandler {
	if process == nil {
		process = func(message []byte) []byte { return message }
	}
	return &socketHandler{clients: make(map[*websocket.Conn]context.CancelFunc), process: process}
}

// Accept's default check verifies origin hosts. This example also requires the
// scheme and port to match and rejects malformed or duplicated Origin headers.
// Clients without Origin are allowed; Origin is not an authentication method.
func sameOrigin(r *http.Request) bool {
	values := r.Header.Values("Origin")
	if len(values) == 0 {
		return true
	}
	if len(values) != 1 || values[0] == "" {
		return false
	}
	origin, err := url.Parse(values[0])
	if err != nil || origin.Host == "" || origin.User != nil || values[0] != origin.Scheme+"://"+origin.Host {
		return false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return origin.Scheme == scheme && strings.EqualFold(origin.Host, r.Host)
}

func (h *socketHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	h.mu.Lock()
	if h.closing || h.count >= maxConnections {
		h.mu.Unlock()
		http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
		return
	}
	h.count++
	h.active.Add(1)
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.count--
		h.mu.Unlock()
		h.active.Done()
	}()

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return // Accept already wrote its HTTP response.
	}
	defer conn.CloseNow()
	conn.SetReadLimit(maxMessageBytes)
	// Hijacked connections need an application-owned lifetime. The original
	// HTTP request context is not used after the protocol upgrade.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.mu.Lock()
	if h.closing {
		h.mu.Unlock()
		return
	}
	h.clients[conn] = cancel
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.clients, conn)
		h.mu.Unlock()
	}()
	defer func() {
		if recover() != nil {
			// Panic values may contain credentials or message contents. Never
			// send or log them, and never write HTTP after the upgrade.
			slog.Error("websocket handler panic")
			_ = conn.Close(websocket.StatusInternalError, "internal error")
		}
	}()
	for {
		read, stopRead := context.WithTimeout(ctx, readTimeout)
		kind, message, err := conn.Read(read)
		stopRead()
		if err != nil {
			return
		}
		response := h.process(message)
		write, stopWrite := context.WithTimeout(ctx, writeTimeout)
		err = conn.Write(write, kind, response)
		stopWrite()
		if err != nil {
			return
		}
	}
}

func newRouter(handler *socketHandler) *gola.Engine {
	r := gola.Default()
	r.GET("/ws", gola.WrapH(handler))
	r.GET("/", func(c *gola.Context) {
		_ = c.String(http.StatusOK, "GoLa WebSocket echo endpoint: /ws\n")
	})
	return r
}

// Shutdown first gates new handlers, then closes every upgraded connection.
// net/http.Server.Shutdown does not wait for or close hijacked connections.
func (h *socketHandler) Shutdown(ctx context.Context) error {
	h.mu.Lock()
	h.closing = true
	clients := make(map[*websocket.Conn]context.CancelFunc, len(h.clients))
	for conn, cancel := range h.clients {
		clients[conn] = cancel
	}
	h.mu.Unlock()
	var closing sync.WaitGroup
	for conn := range clients {
		closing.Go(func() { _ = conn.Close(websocket.StatusGoingAway, "server shutting down") })
	}
	finished := make(chan struct{})
	go func() {
		closing.Wait()
		h.active.Wait()
		close(finished)
	}()
	select {
	case <-finished:
		return nil
	case <-ctx.Done():
		// Cancelling an active Read/Write closes its underlying connection.
		// Join cleanup before returning so no shutdown worker is abandoned.
		for _, cancel := range clients {
			cancel()
		}
		<-finished
		return ctx.Err()
	}
}

func serve(ctx context.Context, listener net.Listener, handler *socketHandler) error {
	server := &http.Server{
		Handler: newRouter(handler), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second,
		IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20,
	}
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(listener) }()
	select {
	case err := <-finished:
		shutdown, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		return errors.Join(err, handler.Shutdown(shutdown))
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	// Start HTTP shutdown concurrently: it closes the listener immediately,
	// while the WebSocket registry handles the separately owned connections.
	httpDone := make(chan error, 1)
	go func() { httpDone <- server.Shutdown(shutdown) }()
	wsErr := handler.Shutdown(shutdown)
	httpErr := <-httpDone
	if httpErr != nil {
		httpErr = errors.Join(httpErr, server.Close())
	}
	serveErr := <-finished
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	return errors.Join(wsErr, httpErr, serveErr)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	listener, err := net.Listen("tcp", "127.0.0.1:8080")
	if err == nil {
		fmt.Println("WebSocket echo: ws://127.0.0.1:8080/ws")
		err = serve(ctx, listener, newSocketHandler(nil))
	}
	if err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
