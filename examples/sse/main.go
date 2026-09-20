package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/starstack/gola"
)

func newRouter() *gola.Engine {
	r := gola.Default()
	r.GET("/events", func(c *gola.Context) {
		controller := http.NewResponseController(c.Writer)
		send := func(event string, value any) error {
			// The entire stream has no lifetime write timeout, but each write is
			// bounded so a client that stops reading cannot retain it forever.
			if err := controller.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
				return err
			}
			if err := c.SSEvent(event, value); err != nil {
				return err
			}
			// Do not let the deadline expire while idle between heartbeats.
			return controller.SetWriteDeadline(time.Time{})
		}
		if err := send("ready", gola.H{"status": "connected"}); err != nil {
			c.Fail(err)
			return
		}
		heartbeat := time.NewTicker(15 * time.Second)
		defer heartbeat.Stop()
		for {
			select {
			case <-c.Request.Context().Done():
				return
			case now := <-heartbeat.C:
				if err := send("heartbeat", gola.H{"time": now.UTC().Format(time.RFC3339)}); err != nil {
					c.Error(err)
					return
				}
			}
		}
	})
	return r
}

func serve(ctx context.Context, listener net.Listener) error {
	// Cancellation is deliberately propagated to every SSE request so Shutdown
	// can finish long-lived handlers. No detached stream goroutines are created.
	server := &http.Server{
		Handler: newRouter(), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 15 * time.Second, WriteTimeout: 0,
		IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(listener) }()
	select {
	case err := <-finished:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	shutdownErr := server.Shutdown(shutdown)
	if shutdownErr != nil {
		shutdownErr = errors.Join(shutdownErr, server.Close())
	}
	serveErr := <-finished
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	return errors.Join(shutdownErr, serveErr)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	listener, err := net.Listen("tcp", "127.0.0.1:8080")
	if err == nil {
		fmt.Println("SSE: curl -N http://127.0.0.1:8080/events")
		err = serve(ctx, listener)
	}
	if err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
