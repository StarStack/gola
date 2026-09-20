// Package compress provides opt-in streaming gzip for standard HTTP handlers.
// Do not enable compression for responses combining secrets with attacker input.
package compress

import (
	"compress/gzip"
	"fmt"
	"mime"
	"net/http"
	"strconv"
	"strings"
)

// Config selects the gzip level. Zero uses gzip.DefaultCompression; levels
// gzip.HuffmanOnly, gzip.DefaultCompression, and 1 through 9 are supported.
type Config struct{ Level int }

// New returns a standard net/http middleware. Compression is never installed
// by GoLa automatically. Apply it only to routes whose content may be compressed.
// Bodies are streamed, without buffering the full response or a minimum size.
func New(config Config) (func(http.Handler) http.Handler, error) {
	level := config.Level
	if level == 0 {
		level = gzip.DefaultCompression
	}
	if level < gzip.HuffmanOnly || level > gzip.BestCompression {
		return nil, fmt.Errorf("compress: invalid gzip level %d", config.Level)
	}
	return func(next http.Handler) http.Handler {
		if next == nil {
			panic("compress: nil handler")
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Vary also applies to identity responses so shared caches cannot reuse
			// an uncompressed variant without considering Accept-Encoding.
			addVary(w.Header())
			eligible := r.Method != http.MethodHead && r.Method != http.MethodConnect && len(r.Header.Values("Range")) == 0 && len(r.Header.Values("Upgrade")) == 0 && !hasToken(r.Header.Values("Connection"), "upgrade") && acceptsGzip(r.Header.Values("Accept-Encoding"))
			cw := &writer{underlying: w, level: level, eligible: eligible}
			next.ServeHTTP(cw, r)
			// Intentionally not deferred: a panic or ErrAbortHandler must not
			// finish a truncated response or replace the original panic.
			if cw.gzip != nil {
				if err := cw.gzip.Close(); err != nil {
					panic(http.ErrAbortHandler)
				}
			}
		})
	}, nil
}

type writer struct {
	underlying http.ResponseWriter
	level      int
	status     int
	gzip       *gzip.Writer
	eligible   bool
}

func (w *writer) Header() http.Header         { return w.underlying.Header() }
func (w *writer) Unwrap() http.ResponseWriter { return w.underlying }

func (w *writer) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	if status < 100 || status > 999 {
		panic("compress: invalid HTTP status code")
	}
	if status >= 100 && status < 200 && status != http.StatusSwitchingProtocols {
		w.underlying.WriteHeader(status)
		return
	}
	w.status = status
	h := w.Header()
	addVary(h)
	mediaType, _, _ := mime.ParseMediaType(h.Get("Content-Type"))
	// Explicit WriteHeader without a content type is left to net/http's normal
	// body sniffing. Sniffing compressed bytes would misidentify the media type.
	if w.eligible && status >= 200 && status != http.StatusNoContent && status != http.StatusResetContent && status != http.StatusNotModified && status != http.StatusPartialContent && h.Get("Content-Type") != "" && len(h.Values("Content-Encoding")) == 0 && len(h.Values("Content-Range")) == 0 && !strings.EqualFold(mediaType, "text/event-stream") && !hasToken(h.Values("Cache-Control"), "no-transform") && !hasToken(h.Values("Trailer"), "Content-Digest") && !hasToken(h.Values("Trailer"), "Content-MD5") {
		h.Del("Content-Length")
		h.Del("Content-MD5")
		h.Del("Content-Digest")
		h.Del("Accept-Ranges")
		h.Set("Content-Encoding", "gzip")
		if etag := h.Get("ETag"); strings.HasPrefix(etag, "\"") {
			h.Set("ETag", "W/"+etag)
		}
		// The level was validated by New.
		w.gzip, _ = gzip.NewWriterLevel(w.underlying, w.level)
	}
	w.underlying.WriteHeader(status)
}

func (w *writer) Write(p []byte) (int, error) {
	if w.status == 0 {
		if _, exists := w.Header()["Content-Type"]; !exists && len(p) != 0 {
			w.Header().Set("Content-Type", http.DetectContentType(p))
		}
		w.WriteHeader(http.StatusOK)
	}
	if w.gzip != nil {
		return w.gzip.Write(p)
	}
	return w.underlying.Write(p)
}

// FlushError flushes both gzip and the transport. ResponseController uses this
// method and follows Unwrap for deadlines, full duplex and connection hijacking.
func (w *writer) FlushError() error {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if w.gzip != nil {
		if err := w.gzip.Flush(); err != nil {
			return err
		}
	}
	return http.NewResponseController(w.underlying).Flush()
}

func (w *writer) Flush() { _ = w.FlushError() }

func addVary(h http.Header) {
	if !hasToken(h.Values("Vary"), "*") && !hasToken(h.Values("Vary"), "Accept-Encoding") {
		h.Add("Vary", "Accept-Encoding")
	}
}

func hasToken(values []string, token string) bool {
	for _, value := range values {
		for part := range strings.SplitSeq(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

// Invalid/duplicate quality parameters fail closed. An explicit gzip exclusion
// takes precedence over a wildcard, including across multiple field lines.
func acceptsGzip(values []string) bool {
	var gzipSeen, wildcardSeen bool
	gzipQ, wildcardQ := 1.0, 1.0
	for _, value := range values {
		for item := range strings.SplitSeq(value, ",") {
			parts := strings.Split(item, ";")
			coding := strings.TrimSpace(parts[0])
			if !strings.EqualFold(coding, "gzip") && coding != "*" {
				continue
			}
			q := 1.0
			if len(parts) > 1 {
				q = 0
				name, value, ok := strings.Cut(strings.TrimSpace(parts[1]), "=")
				if ok && len(parts) == 2 && strings.EqualFold(strings.TrimSpace(name), "q") {
					if quality, valid := parseQuality(strings.TrimSpace(value)); valid {
						q = quality
					}
				}
			}
			if strings.EqualFold(coding, "gzip") {
				gzipSeen, gzipQ = true, min(gzipQ, q)
			} else {
				wildcardSeen, wildcardQ = true, min(wildcardQ, q)
			}
		}
	}
	if gzipSeen {
		return gzipQ > 0
	}
	return wildcardSeen && wildcardQ > 0
}

func parseQuality(value string) (float64, bool) {
	if len(value) == 0 || (value[0] != '0' && value[0] != '1') || len(value) > 5 {
		return 0, false
	}
	if len(value) > 1 {
		if value[1] != '.' {
			return 0, false
		}
		for _, digit := range value[2:] {
			if digit < '0' || digit > '9' || (value[0] == '1' && digit != '0') {
				return 0, false
			}
		}
	}
	q, err := strconv.ParseFloat(value, 64)
	return q, err == nil
}
