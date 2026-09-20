package gola

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"time"
)

// RequestIDKey is the Context key containing the server-generated request ID.
const RequestIDKey = "gola.request_id"

// RequestID assigns a fresh 128-bit identifier. Incoming IDs are not trusted.
func RequestID() HandlerFunc {
	return func(c *Context) {
		var value [16]byte
		_, _ = rand.Read(value[:]) // crypto/rand.Read never returns an error on supported Go versions.
		id := hex.EncodeToString(value[:])
		c.Set(RequestIDKey, id)
		c.Writer.Header().Set("X-Request-ID", id)
	}
}

// Logger records one structured access event, including on panic and abort.
// Query strings, request bodies, cookies and authorization headers are omitted.
func Logger() HandlerFunc {
	return func(c *Context) {
		start := time.Now()
		completed := false
		defer func() {
			status := c.Writer.Status()
			if status == 0 {
				status = http.StatusOK
			}
			attrs := []slog.Attr{
				slog.String("request_id", requestID(c)),
				slog.String("method", c.Request.Method),
				slog.String("route", c.FullPath()),
				slog.Int("status", status),
				slog.Duration("duration", time.Since(start)),
				slog.Int("response_bytes", c.Writer.Size()),
				slog.String("client_ip", c.ClientIP()),
				slog.Bool("aborted_response", !completed),
			}
			if c.FullPath() == "" {
				// Quote explicitly even when an application installs a custom slog handler.
				path := c.Request.URL.Path
				if len(path) > 512 {
					path = path[:512] + "…"
				}
				attrs = append(attrs, slog.String("path", strconv.QuoteToASCII(path)))
			}
			c.engine.logger.LogAttrs(c.Request.Context(), slog.LevelInfo, "http request", attrs...)
		}()
		c.Next()
		completed = true
	}
}

// Recovery recovers handler panics before response commitment. After commitment
// it delegates truncation handling to net/http through http.ErrAbortHandler.
func Recovery() HandlerFunc {
	return func(c *Context) {
		defer func() {
			if value := recover(); value != nil {
				c.Abort()
				if value == http.ErrAbortHandler {
					panic(value)
				}
				// Do not dump the request or format arbitrary panic values: both can
				// contain credentials, body data, or attacker-controlled strings.
				c.engine.logger.ErrorContext(c.Request.Context(), "handler panic",
					"request_id", requestID(c), "panic_type", fmt.Sprintf("%T", value),
					"stack", string(debug.Stack()))
				c.Error(errors.New("gola: handler panic"))
				if c.Writer.Written() {
					panic(http.ErrAbortHandler)
				}
				// Recovery's response is deliberately generic, independent of custom
				// business error handlers that might expose an internal error.
				if err := c.String(http.StatusInternalServerError, "Internal Server Error\n"); err != nil {
					c.Error(err)
				}
			}
		}()
		c.Next()
	}
}

// BodyLimit bounds actual reads, including requests of unknown length. A handler
// must handle *http.MaxBytesError if an unknown-length body exceeds the limit.
func BodyLimit(maxBytes int64) HandlerFunc {
	if maxBytes <= 0 {
		panic("gola: body limit must be positive")
	}
	return func(c *Context) {
		if c.Request.ContentLength > maxBytes {
			c.Error(&http.MaxBytesError{Limit: maxBytes})
			c.AbortWithStatus(http.StatusRequestEntityTooLarge)
			return
		}
		if c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		}
	}
}

func requestID(c *Context) string {
	value, _ := c.Get(RequestIDKey)
	id, _ := value.(string)
	return id
}
