// Use a standard http.Server when the application owns process lifecycle.
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
	r.GET("/work", func(c *gola.Context) {
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		select {
		case <-timer.C:
			if err := c.JSON(http.StatusOK, map[string]string{"status": "completed"}); err != nil {
				c.Error(err)
			}
		case <-c.Request.Context().Done():
			// Pass this same context into downstream HTTP calls and other work.
			// Stop work on disconnect or forced shutdown; do not retry writes.
			return
		}
	})
	return r
}

func serve(ctx context.Context, addr string, handler http.Handler, grace time.Duration) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	// The signal starts draining; it must not immediately cancel in-flight work.
	requestBase, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()
	server := &http.Server{
		Handler: handler, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second,
		IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20,
		BaseContext: func(net.Listener) context.Context { return requestBase },
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	select {
	case err := <-serveDone:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}
	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), grace)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownContext); err != nil {
		cancelRequests()
		closeErr := server.Close()
		serveErr := <-serveDone
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		return errors.Join(fmt.Errorf("shutdown: %w", err), closeErr, serveErr)
	}
	if err := <-serveDone; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Hijacked connections such as WebSockets need separate application cleanup.
	if err := serve(ctx, "127.0.0.1:8080", newRouter(), 10*time.Second); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
