package metrics

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/starstack/gola"
)

func collector(t *testing.T, maximum int) *Collector {
	t.Helper()
	m, err := New(Config{MaxSeries: maximum})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRouteTemplatesAndTotals(t *testing.T) {
	m := collector(t, 0)
	e := gola.New()
	e.Use(m.Middleware())
	e.GET("/users/:id", func(c *gola.Context) { _ = c.String(201, "hello") })
	for i := range 50 {
		e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", fmt.Sprintf("/users/%d?secret=private", i), nil))
		e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", fmt.Sprintf("/missing/%d", i), nil))
	}
	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/users/a", nil))
	snapshot := m.Snapshot()
	if snapshot.InFlight != 0 || len(snapshot.Series) != 3 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	for _, s := range snapshot.Series {
		switch s.Route {
		case "/users/:id":
			if s.Requests != 50 || s.Status != 201 || s.ResponseBytes != 250 || s.DurationSeconds <= 0 {
				t.Fatalf("route totals=%+v", s)
			}
		case "_not_found":
			if s.Requests != 50 || s.Status != 404 {
				t.Fatalf("404 totals=%+v", s)
			}
		case "_method_not_allowed":
			if s.Requests != 1 || s.Status != 405 {
				t.Fatalf("405 totals=%+v", s)
			}
		default:
			t.Fatalf("unexpected label=%+v", s)
		}
	}
	snapshot.Series[0].Requests = 999
	if m.Snapshot().Series[0].Requests == 999 {
		t.Fatal("snapshot aliases collector state")
	}
}

func TestBoundedSeriesAndUnknownMethods(t *testing.T) {
	m := collector(t, 2)
	e := gola.New()
	e.Use(m.Middleware())
	e.GET("/one", func(c *gola.Context) { c.Status(200) })
	e.GET("/two", func(c *gola.Context) { c.Status(200) })
	e.GET("/three", func(c *gola.Context) { c.Status(200) })
	for _, path := range []string{"/one", "/two", "/three"} {
		e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", path, nil))
	}
	for i := range 30 {
		e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(fmt.Sprintf("METHOD%d", i), "/missing", nil))
	}
	snapshot := m.Snapshot()
	if len(snapshot.Series) != 3 || snapshot.Series[2].Route != "_overflow" || snapshot.Series[2].Requests != 31 {
		t.Fatalf("series not bounded: %+v", snapshot)
	}
	other := collector(t, 0)
	e2 := gola.New()
	e2.Use(other.Middleware())
	for i := range 30 {
		e2.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(fmt.Sprintf("METHOD%d", i), "/missing", nil))
	}
	if s := other.Snapshot(); len(s.Series) != 1 || s.Series[0].Method != "OTHER" || s.Series[0].Requests != 30 {
		t.Fatalf("method cardinality not bounded: %+v", s)
	}
}

func TestInFlightAndConcurrentSnapshot(t *testing.T) {
	m := collector(t, 0)
	entered, release := make(chan struct{}, 100), make(chan struct{})
	e := gola.New()
	e.Use(m.Middleware())
	e.GET("/", func(c *gola.Context) { entered <- struct{}{}; <-release; c.Status(200) })
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
		}()
	}
	for range 100 {
		<-entered
	}
	if s := m.Snapshot(); s.InFlight != 100 || len(s.Series) != 0 {
		t.Fatalf("in flight snapshot=%+v", s)
	}
	close(release)
	for range 100 {
		_ = m.Snapshot()
	}
	wg.Wait()
	if s := m.Snapshot(); s.InFlight != 0 || len(s.Series) != 1 || s.Series[0].Requests != 100 {
		t.Fatalf("completed snapshot=%+v", s)
	}
}

func TestPanicAndRecoveryAccounting(t *testing.T) {
	for _, recovered := range []bool{false, true} {
		m := collector(t, 0)
		e := gola.New(gola.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
		e.Use(m.Middleware())
		if recovered {
			e.Use(gola.Recovery())
		}
		e.GET("/", func(c *gola.Context) { panic("secret") })
		func() {
			defer func() {
				if got := recover(); !recovered && got != "secret" {
					t.Fatalf("panic changed: %v", got)
				}
			}()
			e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
		}()
		s := m.Snapshot()
		if s.InFlight != 0 || len(s.Series) != 1 {
			t.Fatalf("panic leaked in flight: %+v", s)
		}
		if recovered && (s.Series[0].Status != 500 || s.Series[0].Panics != 0) || !recovered && (s.Series[0].Status != 0 || s.Series[0].Panics != 1) {
			t.Fatalf("panic totals=%+v", s)
		}
	}
}

func TestJSONEndpoint(t *testing.T) {
	m := collector(t, 0)
	e := gola.New()
	e.Use(m.Middleware())
	e.GET("/quote/\"/:id", func(c *gola.Context) { c.Status(200) })
	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/quote/%22/value?token=secret", nil))
	w := httptest.NewRecorder()
	m.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	var snapshot Snapshot
	if err := json.Unmarshal(w.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Series) != 1 || snapshot.Series[0].Route != "/quote/\"/:id" || strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), "value") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("invalid JSON export: %q", w.Body.String())
	}
	for _, method := range []string{"HEAD", "POST"} {
		w := httptest.NewRecorder()
		m.ServeHTTP(w, httptest.NewRequest(method, "/", nil))
		if w.Body.Len() != 0 || method == "POST" && (w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "GET, HEAD") {
			t.Fatalf("%s: %d %s", method, w.Code, w.Body.String())
		}
	}
	for _, bound := range []int{-1, 100_001} {
		if _, err := New(Config{MaxSeries: bound}); err == nil {
			t.Fatal("invalid series bound accepted")
		}
	}
}
