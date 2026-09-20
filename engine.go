// Package gola provides a small HTTP framework built directly on net/http,
// with routing, context and middleware. Configure an Engine before serving requests.
package gola

import (
	"html/template"
	"log/slog"
	"net/http"
	"net/netip"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const Version = "0.1.0"
const DefaultBodyLimit int64 = 1 << 20

// DefaultMultipartMemory is the file content memory threshold for multipart
// parsing. The standard library also reserves memory for fields and metadata.
const DefaultMultipartMemory int64 = 1 << 20

type H map[string]any
type HandlerFunc func(*Context)
type ErrorHandler func(*Context, error)
type Option func(*Engine)
type RouteInfo struct{ Method, Path string }

// Engine is configured sequentially, then serves concurrent requests. The first
// request freezes all configuration; subsequent mutation panics.
type Engine struct {
	RouterGroup
	trees                      map[string]*routeNode
	routes                     []*route
	noRoute, noMethod          []HandlerFunc
	notFoundChain, methodChain []HandlerFunc
	once                       sync.Once
	frozen                     atomic.Bool
	logger                     *slog.Logger
	errorHandler               ErrorHandler
	validator                  Validator
	bodyLimit                  int64
	multipartMemory            int64
	disallowUnknownFields      bool
	trustedProxies             []netip.Prefix
	trustRealIP                bool
	htmlTemplates              *template.Template
}

func New(opts ...Option) *Engine {
	e := &Engine{trees: make(map[string]*routeNode), logger: slog.Default(), errorHandler: defaultErrorHandler, bodyLimit: DefaultBodyLimit, multipartMemory: DefaultMultipartMemory}
	e.RouterGroup = RouterGroup{engine: e}
	for _, opt := range opts {
		if opt == nil {
			panic("gola: nil option")
		}
		opt(e)
	}
	return e
}
func Default(opts ...Option) *Engine {
	e := New(opts...)
	e.Use(RequestID(), Logger(), Recovery(), BodyLimit(e.bodyLimit))
	return e
}
func (e *Engine) assertMutable() {
	if e.frozen.Load() {
		panic("gola: engine configuration is frozen")
	}
}
func WithLogger(logger *slog.Logger) Option {
	return func(e *Engine) {
		e.assertMutable()
		if logger == nil {
			panic("gola: nil logger")
		}
		e.logger = logger
	}
}
func WithErrorHandler(h ErrorHandler) Option {
	return func(e *Engine) {
		e.assertMutable()
		if h == nil {
			panic("gola: nil error handler")
		}
		e.errorHandler = h
	}
}
func WithValidator(v Validator) Option {
	return func(e *Engine) {
		e.assertMutable()
		if isNil(v) {
			panic("gola: nil validator")
		}
		e.validator = v
	}
}
func WithBodyLimit(n int64) Option {
	return func(e *Engine) {
		e.assertMutable()
		if n <= 0 {
			panic("gola: body limit must be positive")
		}
		e.bodyLimit = n
	}
}

// WithMultipartMemory sets the memory threshold for multipart file content;
// excess content is stored in temporary files. Zero stores nonempty files on
// disk. This does not change the total request body limit set by WithBodyLimit.
func WithMultipartMemory(n int64) Option {
	return func(e *Engine) {
		e.assertMutable()
		if n < 0 {
			panic("gola: multipart memory threshold must not be negative")
		}
		e.multipartMemory = n
	}
}
func WithDisallowUnknownFields(enabled bool) Option {
	return func(e *Engine) { e.assertMutable(); e.disallowUnknownFields = enabled }
}
func WithTrustedRealIP(enabled bool) Option {
	return func(e *Engine) { e.assertMutable(); e.trustRealIP = enabled }
}
func isNil(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	}
	return false
}
func (e *Engine) NoRoute(h ...HandlerFunc) {
	e.assertMutable()
	checkHandlers(h)
	e.noRoute = append([]HandlerFunc(nil), h...)
}
func (e *Engine) NoMethod(h ...HandlerFunc) {
	e.assertMutable()
	checkHandlers(h)
	e.noMethod = append([]HandlerFunc(nil), h...)
}
func (e *Engine) Routes() []RouteInfo {
	out := make([]RouteInfo, len(e.routes))
	for i, r := range e.routes {
		out[i] = RouteInfo{r.method, r.path}
	}
	return out
}
func (e *Engine) freeze() {
	e.once.Do(func() {
		e.frozen.Store(true)
		for _, r := range e.routes {
			r.chain = append(r.group.chain(), r.handlers...)
		}
		e.notFoundChain = append(e.RouterGroup.chain(), e.noRoute...)
		e.notFoundChain = append(e.notFoundChain, func(c *Context) {
			if !c.Writer.Written() {
				_ = c.String(http.StatusNotFound, "404 page not found\n")
			}
		})
		e.methodChain = append(e.RouterGroup.chain(), e.noMethod...)
		e.methodChain = append(e.methodChain, func(c *Context) {
			if !c.Writer.Written() {
				_ = c.String(http.StatusMethodNotAllowed, "405 method not allowed\n")
			}
		})
	})
}
func (e *Engine) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	e.freeze()
	c := &Context{Request: req, Writer: &ResponseWriter{writer: w, method: req.Method, status: http.StatusOK}, engine: e}
	defer c.cleanupMultipart()
	parts := splitPath(req.URL.Path)
	var matched *route
	var values []string
	if root := e.trees[req.Method]; root != nil {
		matched, values = root.match(parts, 0, nil)
	}
	if matched != nil {
		c.fullPath, c.handlers = matched.path, matched.chain
		if len(values) > 0 {
			c.params = make(map[string]string, len(values))
			for i, v := range values {
				c.params[matched.names[i]] = v
				req.SetPathValue(matched.names[i], v)
			}
		}
	} else {
		var allow []string
		for method, root := range e.trees {
			if method == req.Method {
				continue
			}
			if r, _ := root.match(parts, 0, nil); r != nil {
				allow = append(allow, method)
			}
		}
		if len(allow) > 0 {
			sort.Strings(allow)
			w.Header().Set("Allow", strings.Join(allow, ", "))
			c.Writer.status = http.StatusMethodNotAllowed
			c.handlers = e.methodChain
		} else {
			c.Writer.status = http.StatusNotFound
			c.handlers = e.notFoundChain
		}
	}
	c.Next()
	if !c.Writer.Written() && !c.standardHandler {
		c.Writer.WriteHeader(c.Writer.Status())
	}
}

// Run uses conservative defaults for ordinary APIs. Construct http.Server for
// application-specific timeouts, TLS, signals and graceful shutdown.
func (e *Engine) Run(addr string) error {
	return (&http.Server{Addr: addr, Handler: e, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}).ListenAndServe()
}
