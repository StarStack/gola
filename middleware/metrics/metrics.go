// Package metrics collects bounded request metrics without external dependencies.
package metrics

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/starstack/gola"
)

// Config bounds memory independently of the number of requests or URL values.
type Config struct {
	// MaxSeries defaults to 1,024 and must not exceed 100,000. Overflow has
	// one additional fixed series, aggregating all excess label combinations.
	MaxSeries int
}

// Series identifies requests by route template, normalized method and status.
// Status zero means a panic escaped before a final response was committed.
type Series struct {
	Route           string  `json:"route"`
	Method          string  `json:"method"`
	Status          int     `json:"status"`
	Requests        uint64  `json:"requests"`
	Panics          uint64  `json:"panics"`
	DurationSeconds float64 `json:"duration_seconds"`
	ResponseBytes   uint64  `json:"response_bytes"`
}

// Snapshot is a consistent copy of the collector; callers may freely modify it.
// DurationSeconds and ResponseBytes are cumulative totals, not averages.
type Snapshot struct {
	InFlight int64    `json:"in_flight"`
	Series   []Series `json:"series"`
}

type labels struct {
	route, method string
	status        int
}

// Collector is safe for concurrent requests and snapshots. Its JSON handler
// should be mounted only on an authenticated administrative route.
type Collector struct {
	mu       sync.Mutex
	max      int
	inFlight int64
	series   map[labels]Series
	overflow Series
}

func New(config Config) (*Collector, error) {
	if config.MaxSeries == 0 {
		config.MaxSeries = 1024
	}
	if config.MaxSeries < 1 || config.MaxSeries > 100_000 {
		return nil, fmt.Errorf("metrics: invalid maximum series")
	}
	return &Collector{max: config.MaxSeries, series: make(map[labels]Series), overflow: Series{Route: "_overflow", Method: "OTHER"}}, nil
}

// Middleware measures completed requests and panics. Install it before Recovery
// to observe Recovery's final 500 status, and before limiters to count rejections.
// Bytes are application bytes accepted by GoLa, before any outer compression.
func (m *Collector) Middleware() gola.HandlerFunc {
	return func(c *gola.Context) {
		started := time.Now()
		m.mu.Lock()
		m.inFlight++
		m.mu.Unlock()
		completed := false
		defer func() {
			status := c.Writer.Status()
			if !completed && !c.Writer.Written() {
				status = 0
			}
			route := c.FullPath()
			if route == "" {
				switch status {
				case http.StatusNotFound:
					route = "_not_found"
				case http.StatusMethodNotAllowed:
					route = "_method_not_allowed"
				default:
					route = "_unmatched"
				}
			}
			label := labels{route, normalizeMethod(c.Request.Method), status}
			m.mu.Lock()
			defer m.mu.Unlock()
			m.inFlight--
			value, exists := m.series[label]
			overflow := !exists && len(m.series) == m.max
			if overflow {
				value = m.overflow
			} else if !exists {
				value = Series{Route: label.route, Method: label.method, Status: label.status}
			}
			value.Requests++
			if !completed {
				value.Panics++
			}
			value.DurationSeconds += time.Since(started).Seconds()
			value.ResponseBytes += uint64(max(0, c.Writer.Size()))
			if overflow {
				m.overflow = value
			} else {
				m.series[label] = value
			}
		}()
		c.Next()
		completed = true
	}
}

func normalizeMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodOptions, http.MethodConnect, http.MethodTrace:
		return method
	default:
		return "OTHER"
	}
}

func (m *Collector) Snapshot() Snapshot {
	m.mu.Lock()
	result := Snapshot{InFlight: m.inFlight, Series: make([]Series, 0, len(m.series)+1)}
	for _, value := range m.series {
		result.Series = append(result.Series, value)
	}
	if m.overflow.Requests != 0 {
		result.Series = append(result.Series, m.overflow)
	}
	m.mu.Unlock()
	slices.SortFunc(result.Series, func(a, b Series) int {
		if cmp := strings.Compare(a.Route, b.Route); cmp != 0 {
			return cmp
		}
		if cmp := strings.Compare(a.Method, b.Method); cmp != 0 {
			return cmp
		}
		return a.Status - b.Status
	})
	return result
}

// ServeHTTP exports a JSON snapshot for GET/HEAD. No metrics endpoint is mounted
// automatically, and no URL, query, user identity or request header is retained.
func (m *Collector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_ = json.NewEncoder(w).Encode(m.Snapshot())
}
