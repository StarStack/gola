// Package golaotel adapts GoLa request handling to an application-owned
// OpenTelemetry provider. Exporters and provider shutdown belong to the app.
package golaotel

import (
	"fmt"
	"net/http"
	"reflect"

	"github.com/starstack/gola"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

type Config struct {
	TracerProvider trace.TracerProvider
	// Nil disables extraction. Set TraceContext explicitly at a trusted edge
	// when remote trace IDs and sampling decisions should be accepted.
	Propagator propagation.TextMapPropagator
}

func New(config Config) (gola.HandlerFunc, error) {
	if config.TracerProvider == nil || nilPointer(config.TracerProvider) {
		return nil, fmt.Errorf("golaotel: tracer provider is required")
	}
	if config.Propagator != nil && nilPointer(config.Propagator) {
		return nil, fmt.Errorf("golaotel: propagator must not be a typed nil")
	}
	tracer := config.TracerProvider.Tracer("github.com/starstack/gola/contrib/otel", trace.WithInstrumentationVersion(gola.Version))
	return func(c *gola.Context) {
		request := c.Request
		ctx := request.Context()
		if config.Propagator != nil {
			ctx = config.Propagator.Extract(ctx, propagation.HeaderCarrier(request.Header))
		}
		method := normalizedMethod(request.Method)
		route := c.FullPath()
		name := method + " unmatched"
		attributes := []attribute.KeyValue{attribute.String("http.request.method", method)}
		if route != "" {
			name = method + " " + route
			attributes = append(attributes, attribute.String("http.route", route))
		}
		ctx, span := tracer.Start(ctx, name, trace.WithSpanKind(trace.SpanKindServer), trace.WithAttributes(attributes...))
		// Keep the request-local replacement after Next: downstream middleware
		// may install body limits or other request fields needed during unwind.
		c.Request = request.WithContext(ctx)
		defer func() {
			failure := recover()
			status := c.Writer.Status()
			if failure != nil {
				if !c.Writer.Written() {
					status = http.StatusInternalServerError
				}
				span.SetStatus(codes.Error, "handler panic")
			} else if status >= 500 {
				span.SetStatus(codes.Error, "server error")
			}
			span.SetAttributes(attribute.Int("http.response.status_code", status))
			span.End()
			if failure != nil {
				panic(failure)
			}
		}()
		c.Next()
	}, nil
}

func nilPointer(value any) bool {
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Func, reflect.Slice, reflect.Chan:
		return v.IsNil()
	default:
		return false
	}
}

func normalizedMethod(method string) string {
	switch method {
	case "GET", "HEAD", "POST", "PUT", "DELETE", "CONNECT", "OPTIONS", "TRACE", "PATCH":
		return method
	default:
		return "_OTHER"
	}
}
