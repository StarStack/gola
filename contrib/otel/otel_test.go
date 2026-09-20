package golaotel_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/starstack/gola"
	golaotel "github.com/starstack/gola/contrib/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func provider(t *testing.T) (*sdktrace.TracerProvider, *tracetest.InMemoryExporter) {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	p := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	return p, exporter
}

func TestTraceContextAndControlledAttributes(t *testing.T) {
	p, exporter := provider(t)
	middleware, err := golaotel.New(golaotel.Config{TracerProvider: p})
	if err != nil {
		t.Fatal(err)
	}
	r := gola.New()
	r.Use(middleware)
	r.GET("/users/:id", func(c *gola.Context) {
		if !trace.SpanFromContext(c.Request.Context()).SpanContext().IsValid() {
			t.Error("span did not reach the handler context")
		}
		_, child := p.Tracer("app").Start(c.Request.Context(), "database work")
		child.End()
		c.Status(201)
	})
	req := httptest.NewRequest("GET", "/users/private-id?token=secret", nil)
	req.Header.Set("Authorization", "Bearer secret")
	r.ServeHTTP(httptest.NewRecorder(), req)
	spans := exporter.GetSpans()
	if len(spans) != 2 || spans[1].Name != "GET /users/:id" || spans[0].Parent.SpanID() != spans[1].SpanContext.SpanID() {
		t.Fatalf("incorrect parent/route spans: %+v", spans)
	}
	for _, attr := range spans[1].Attributes {
		if strings.Contains(attr.Value.Emit(), "private-id") || strings.Contains(attr.Value.Emit(), "secret") {
			t.Fatal("private request data exported as span attributes")
		}
	}
}

func TestRemotePropagationIsExplicit(t *testing.T) {
	const parent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	for _, enabled := range []bool{false, true} {
		p, exporter := provider(t)
		config := golaotel.Config{TracerProvider: p}
		if enabled {
			config.Propagator = propagation.TraceContext{}
		}
		middleware, err := golaotel.New(config)
		if err != nil {
			t.Fatal(err)
		}
		r := gola.New()
		r.Use(middleware)
		req := httptest.NewRequest("GET", "/missing/private", nil)
		req.Header.Set("traceparent", parent)
		r.ServeHTTP(httptest.NewRecorder(), req)
		spans := exporter.GetSpans()
		if len(spans) != 1 || spans[0].Parent.IsRemote() != enabled || spans[0].Name != "GET unmatched" {
			t.Fatalf("unexpected propagation: enabled=%v spans=%+v", enabled, spans)
		}
	}
}

func TestTracePreservesDownstreamRequestWrapping(t *testing.T) {
	p, exporter := provider(t)
	middleware, err := golaotel.New(golaotel.Config{TracerProvider: p})
	if err != nil {
		t.Fatal(err)
	}
	type contextKey struct{}
	r := gola.New()
	r.Use(func(c *gola.Context) {
		c.Next()
		if !trace.SpanFromContext(c.Request.Context()).SpanContext().IsValid() || c.Request.Context().Value(contextKey{}) != "retained" {
			t.Error("unwind lost request-local trace or downstream context")
		}
		remaining, err := io.ReadAll(c.Request.Body)
		var limit *http.MaxBytesError
		if string(remaining) != "4" || !errors.As(err, &limit) || limit.Limit != 4 {
			t.Fatalf("downstream body limit lost: data=%q error=%v", remaining, err)
		}
	})
	r.Use(middleware, gola.BodyLimit(4))
	r.POST("/", func(c *gola.Context) {
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), contextKey{}, "retained"))
		prefix := make([]byte, 3)
		if _, err := io.ReadFull(c.Request.Body, prefix); err != nil || string(prefix) != "123" {
			t.Fatalf("initial body read=%q error=%v", prefix, err)
		}
		c.Status(200)
	})
	req := httptest.NewRequest("POST", "/", strings.NewReader("1234567890"))
	req.ContentLength = -1
	r.ServeHTTP(httptest.NewRecorder(), req)
	if spans := exporter.GetSpans(); len(spans) != 1 {
		t.Fatalf("completed spans=%d", len(spans))
	}
}

func TestPanicsAndConcurrentRequests(t *testing.T) {
	p, exporter := provider(t)
	middleware, err := golaotel.New(golaotel.Config{TracerProvider: p})
	if err != nil {
		t.Fatal(err)
	}
	r := gola.New(gola.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	r.Use(gola.Recovery(), middleware)
	r.GET("/panic", func(*gola.Context) { panic("private failure detail") })
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("GET", "/panic", nil))
			if w.Code != 500 {
				t.Errorf("panic response = %d", w.Code)
			}
		})
	}
	wg.Wait()
	spans := exporter.GetSpans()
	if len(spans) != 12 {
		t.Fatalf("ended spans = %d", len(spans))
	}
	ids := map[trace.SpanID]bool{}
	for _, span := range spans {
		if span.Status.Code != codes.Error || span.Status.Description != "handler panic" || ids[span.SpanContext.SpanID()] {
			t.Fatal("panic not reported safely or trace identity leaked between requests")
		}
		ids[span.SpanContext.SpanID()] = true
	}
	if _, err := golaotel.New(golaotel.Config{}); err == nil {
		t.Fatal("missing provider accepted")
	}
}
