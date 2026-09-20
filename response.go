package gola

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// ResponseWriter tracks final headers and body bytes. Optional transport
// capabilities are accessible through http.ResponseController and Unwrap.
type ResponseWriter struct {
	writer       http.ResponseWriter
	method       string
	status, size int
	written      bool
}

func (w *ResponseWriter) Header() http.Header         { return w.writer.Header() }
func (w *ResponseWriter) Unwrap() http.ResponseWriter { return w.writer }
func (w *ResponseWriter) Status() int                 { return w.status }
func (w *ResponseWriter) Size() int                   { return w.size }
func (w *ResponseWriter) Written() bool               { return w.written }
func (w *ResponseWriter) WriteHeader(status int) {
	if w.written {
		return
	}
	if status < 100 || status > 999 {
		panic("gola: invalid HTTP status code")
	}
	if status >= 100 && status < 200 && status != http.StatusSwitchingProtocols {
		w.writer.WriteHeader(status)
		return
	}
	w.status = status
	w.written = true
	if status == http.StatusNoContent {
		w.Header().Del("Content-Type")
		w.Header().Del("Content-Length")
		w.Header().Del("Transfer-Encoding")
	}
	if status == http.StatusNotModified {
		w.Header().Del("Content-Type")
		w.Header().Del("Transfer-Encoding")
	}
	w.writer.WriteHeader(status)
}
func (w *ResponseWriter) Write(data []byte) (int, error) {
	if !w.written {
		w.WriteHeader(w.status)
	}
	if w.status == http.StatusNoContent || w.status == http.StatusNotModified || w.status < 200 {
		return 0, http.ErrBodyNotAllowed
	}
	if w.method == http.MethodHead {
		return len(data), nil
	}
	n, err := w.writer.Write(data)
	w.size += n
	return n, err
}

// FlushError is recognized by ResponseController without advertising Flusher on
// transports that do not support it. Track the implicit final header as well.
func (w *ResponseWriter) FlushError() error {
	underlying := w.writer
	for {
		switch f := underlying.(type) {
		case interface{ FlushError() error }:
			if !w.written {
				w.WriteHeader(w.status)
			}
			return f.FlushError()
		case http.Flusher:
			if !w.written {
				w.WriteHeader(w.status)
			}
			f.Flush()
			return nil
		case interface{ Unwrap() http.ResponseWriter }:
			underlying = f.Unwrap()
		default:
			return http.ErrNotSupported
		}
	}
}
func (c *Context) Header(key, value string) {
	if value == "" {
		c.Writer.Header().Del(key)
	} else {
		c.Writer.Header().Set(key, value)
	}
}
func (c *Context) Status(status int) { c.Writer.WriteHeader(status) }
func (c *Context) JSON(status int, value any) error {
	if c.Writer.Written() {
		return ErrResponseCommitted
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return c.Data(status, "application/json; charset=utf-8", data)
}
func (c *Context) String(status int, format string, values ...any) error {
	return c.Data(status, "text/plain; charset=utf-8", []byte(fmt.Sprintf(format, values...)))
}
func (c *Context) Data(status int, contentType string, data []byte) error {
	if c.Writer.Written() {
		return ErrResponseCommitted
	}
	if status < 200 || status > 599 {
		return ErrInvalidStatus
	}
	c.Header("Content-Type", contentType)
	c.Writer.WriteHeader(status)
	if c.Request.Method == http.MethodHead || status == 204 || status == 304 {
		return nil
	}
	_, err := c.Writer.Write(data)
	return err
}
func (c *Context) Redirect(status int, location string) error {
	if c.Writer.Written() {
		return ErrResponseCommitted
	}
	if status != 301 && status != 302 && status != 303 && status != 307 && status != 308 {
		return ErrInvalidRedirect
	}
	if strings.ContainsAny(location, "\r\n\x00") {
		return ErrInvalidRedirect
	}
	u, err := url.Parse(location)
	if err != nil || location == "" || strings.HasPrefix(location, "//") || strings.Contains(location, "\\") || u.User != nil {
		return ErrInvalidRedirect
	}
	if u.IsAbs() {
		if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return ErrInvalidRedirect
		}
	} else if !strings.HasPrefix(location, "/") || u.Host != "" {
		return ErrInvalidRedirect
	}
	// Deliberately do not read forwarded prefix/host/protocol headers.
	c.Header("Location", location)
	return c.Data(status, "text/plain; charset=utf-8", nil)
}
