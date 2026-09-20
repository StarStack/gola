package middleware_test

import (
	"bufio"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/starstack/gola"
	"github.com/starstack/gola/middleware/compress"
	"github.com/starstack/gola/middleware/metrics"
	"github.com/starstack/gola/middleware/ratelimit"
)

// Verify the actual transport with the same ordering used by the examples.
// Independent package tests cannot catch wrapper and engine interactions.
func TestComposedHTTPMiddleware(t *testing.T) {
	collector, err := metrics.New(metrics.Config{MaxSeries: 8})
	if err != nil {
		t.Fatal(err)
	}
	limit, err := ratelimit.New(ratelimit.Config{Rate: 0.1, Burst: 1})
	if err != nil {
		t.Fatal(err)
	}
	compression, err := compress.New(compress.Config{})
	if err != nil {
		t.Fatal(err)
	}
	r := gola.New()
	r.Use(collector.Middleware(), gola.Recovery())
	r.GET("/hello/:name", limit, func(c *gola.Context) { c.Error(c.JSON(200, gola.H{"hello": c.Param("name")})) })
	stopped := make(chan struct{})
	r.GET("/events", func(c *gola.Context) {
		defer close(stopped)
		c.Header("Cache-Control", "private, no-store")
		if c.SSEvent("ready", true) != nil {
			return
		}
		<-c.Request.Context().Done()
	})
	s := httptest.NewServer(compression(r))
	defer s.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	get := func(path string) *http.Response {
		t.Helper()
		req, err := http.NewRequest("GET", s.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Accept-Encoding", "gzip")
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	response := get("/hello/Danny")
	if response.StatusCode != 200 || response.Header.Get("Content-Encoding") != "gzip" {
		response.Body.Close()
		t.Fatalf("unexpected compressed response: %v", response)
	}
	z, err := gzip.NewReader(response.Body)
	if err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	body, err := io.ReadAll(z)
	z.Close()
	response.Body.Close()
	if err != nil || string(body) != `{"hello":"Danny"}` {
		t.Fatalf("body=%q, error=%v", body, err)
	}
	response = get("/hello/another")
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != 429 || response.Header.Get("Retry-After") == "" {
		t.Fatal("limit not enforced", response.StatusCode)
	}
	response = get("/events")
	if response.Header.Get("Content-Encoding") != "" || response.Header.Get("Cache-Control") != "private, no-store" {
		response.Body.Close()
		t.Fatal("stream headers changed", response.Header)
	}
	line, err := bufio.NewReader(response.Body).ReadString('\n')
	response.Body.Close()
	if err != nil || strings.TrimSpace(line) != "event: ready" {
		t.Fatalf("event not flushed: %q, %v", line, err)
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("stream ignored disconnect")
	}
	// Wait for the outer collector to run after the stream handler exits.
	deadline := time.Now().Add(5 * time.Second)
	for collector.Snapshot().InFlight != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	snapshot := collector.Snapshot()
	if snapshot.InFlight != 0 {
		t.Fatal("in-flight metric leaked")
	}
	var total uint64
	for _, series := range snapshot.Series {
		total += series.Requests
		if strings.Contains(series.Route, "Danny") || strings.Contains(series.Route, "another") {
			t.Fatal("raw path leaked to metrics")
		}
	}
	if total != 3 {
		t.Fatalf("requests=%d, snapshot=%+v", total, snapshot)
	}
}
