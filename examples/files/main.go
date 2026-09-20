package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/starstack/gola"
)

// This loopback-only demonstration uses in-memory file metadata. Production
// applications must persist an ownership record and authorize each upload and
// download. FileStore confines paths; an opaque file ID is not authorization.
func newRouter(store *gola.FileStore) *gola.Engine {
	r := gola.Default(gola.WithBodyLimit(8<<20), gola.WithMultipartMemory(64<<10))
	var files sync.Map
	r.POST("/upload", func(c *gola.Context) {
		header, err := c.FormFile("file")
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, gola.ErrBodyTooLarge) {
				status = http.StatusRequestEntityTooLarge
			}
			if err := c.AbortWithStatusJSON(status, gola.H{"error": "invalid upload"}); err != nil {
				c.Error(err)
			}
			return
		}
		id := rand.Text()
		name := id + ".upload"
		if err := store.Save(c.Request.Context(), header, name, 7<<20); err != nil {
			if errors.Is(err, gola.ErrFileTooLarge) {
				if err := c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gola.H{"error": "file exceeds 7 MiB"}); err != nil {
					c.Error(err)
				}
			} else {
				c.Fail(err)
			}
			return
		}
		// Neither the upload filename nor a client-provided path enters storage.
		files.Store(id, name)
		if err := c.JSON(http.StatusCreated, gola.H{"id": id, "download": "/files/" + id}); err != nil {
			c.Fail(err)
		}
	})
	download := func(c *gola.Context) {
		value, ok := files.Load(c.Param("id"))
		if !ok {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		if err := c.Attachment(store, value.(string), "download.bin"); err != nil {
			c.Fail(err)
		}
	}
	r.GET("/files/:id", download)
	r.HEAD("/files/:id", download)
	return r
}

func run(ctx context.Context, tempParent string, listen func() (net.Listener, error)) (err error) {
	// Use a dedicated directory, never a source repository or the home directory.
	dir, err := os.MkdirTemp(tempParent, "gola-files-example-")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(dir)) }()
	store, err := gola.NewFileStore(dir)
	if err != nil {
		return err
	}
	// Defers run in reverse order: close the store before removing its directory.
	defer func() { err = errors.Join(err, store.Close()) }()
	listener, err := listen()
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	log.Print("file upload demo listening on http://127.0.0.1:8080; file metadata lasts for this process only")
	return serve(ctx, listener, newRouter(store))
}

func serve(ctx context.Context, listener net.Listener, handler http.Handler) error {
	requestBase, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()
	var requests sync.WaitGroup
	var mu sync.Mutex
	draining := false
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			if draining {
				mu.Unlock()
				http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
				return
			}
			requests.Add(1)
			mu.Unlock()
			defer requests.Done()
			handler.ServeHTTP(w, r)
		}),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
		MaxHeaderBytes: 1 << 20,
		BaseContext:    func(net.Listener) context.Context { return requestBase },
	}
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(listener) }()
	var serveErr error
	serveFinished := false
	select {
	case serveErr = <-finished:
		serveFinished = true
	case <-ctx.Done():
	}
	// Gate new work before waiting; no request can start using FileStore after
	// this point. Requests already in progress retain a grace period to finish.
	mu.Lock()
	draining = true
	mu.Unlock()
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	shutdownErr := server.Shutdown(shutdown)
	var closeErr error
	if shutdownErr != nil {
		cancelRequests()
		closeErr = server.Close()
	}
	if !serveFinished {
		serveErr = <-finished
	}
	requests.Wait()
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	return errors.Join(serveErr, shutdownErr, closeErr)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, "", func() (net.Listener, error) {
		return net.Listen("tcp", "127.0.0.1:8080")
	}); err != nil {
		// run has already closed the store and cleaned its own temporary directory.
		log.Printf("file upload demo stopped: %v", err)
		os.Exit(1)
	}
}
