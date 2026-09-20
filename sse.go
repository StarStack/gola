package gola

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"
)

var ErrInvalidSSEEvent = errors.New("gola: invalid SSE event name")

// SSEvent writes and flushes a named Server-Sent Event whose data is JSON. An
// empty name uses the browser's default message event. A stream starts with 200
// and can receive repeated SSEvent calls from the same request goroutine. Stop
// on any write/flush error or Request.Context cancellation. It does not manage
// reconnection IDs, subscriptions, background goroutines or write deadlines.
func (c *Context) SSEvent(name string, value any) error {
	if c.Writer.Written() && (!c.sseStarted || c.Writer.Header().Get("Content-Type") != "text/event-stream; charset=utf-8") {
		return ErrResponseCommitted
	}
	if c.Request.Method == http.MethodHead {
		return http.ErrBodyNotAllowed
	}
	if strings.ContainsAny(name, "\r\n\x00") || !utf8.ValidString(name) {
		return ErrInvalidSSEEvent
	}
	if err := c.Request.Context().Err(); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if !supportsSSEFlush(c.Writer.Unwrap()) {
		return http.ErrNotSupported
	}
	var frame strings.Builder
	if name != "" {
		frame.WriteString("event: ")
		frame.WriteString(name)
		frame.WriteByte('\n')
	}
	frame.WriteString("data: ")
	frame.Write(data)
	frame.WriteString("\n\n")
	if !c.sseStarted {
		c.Header("Content-Type", "text/event-stream; charset=utf-8")
		if len(c.Writer.Header().Values("Cache-Control")) == 0 {
			c.Header("Cache-Control", "no-cache")
		}
		c.Writer.Header().Del("Content-Length")
		c.Writer.WriteHeader(http.StatusOK)
		c.sseStarted = true
	}
	if _, err := c.Writer.Write([]byte(frame.String())); err != nil {
		return err
	}
	return http.NewResponseController(c.Writer).Flush()
}

func supportsSSEFlush(writer http.ResponseWriter) bool {
	for {
		switch w := writer.(type) {
		case interface{ FlushError() error }, http.Flusher:
			return true
		case interface{ Unwrap() http.ResponseWriter }:
			writer = w.Unwrap()
		default:
			return false
		}
	}
}
