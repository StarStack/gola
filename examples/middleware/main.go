// Run with go run ./examples/middleware. Both listeners are loopback-only.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/starstack/gola"
	"github.com/starstack/gola/middleware/compress"
	"github.com/starstack/gola/middleware/metrics"
	"github.com/starstack/gola/middleware/ratelimit"
)

func main() {
	collector, err := metrics.New(metrics.Config{})
	if err != nil {
		log.Fatal(err)
	}
	limit, err := ratelimit.New(ratelimit.Config{Rate: 10, Burst: 20})
	if err != nil {
		log.Fatal(err)
	}
	gzipMiddleware, err := compress.New(compress.Config{})
	if err != nil {
		log.Fatal(err)
	}
	r := gola.New()
	r.Use(collector.Middleware(), gola.RequestID(), gola.Recovery(), gola.BodyLimit(8<<20), limit)
	r.GET("/hello/:name", func(c *gola.Context) {
		_ = c.JSON(http.StatusOK, gola.H{"hello": c.Param("name")})
	})
	app := &http.Server{Addr: "127.0.0.1:8080", Handler: gzipMiddleware(r), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: time.Minute}
	admin := &http.Server{Addr: "127.0.0.1:9090", Handler: collector, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: time.Minute}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	failures := make(chan error, 2)
	for _, server := range []*http.Server{app, admin} {
		go func() { failures <- server.ListenAndServe() }()
	}
	log.Print("GoLa: http://127.0.0.1:8080/hello/Danny; metrics: http://127.0.0.1:9090/")
	select {
	case <-ctx.Done():
	case err := <-failures:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Printf("server stopped: %v", err)
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, server := range []*http.Server{app, admin} {
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
		}
	}
}
