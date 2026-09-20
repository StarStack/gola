package gola

import "strings"

// RouterGroup retains its parent until the engine freezes, so late Use calls
// protect all routes in that scope regardless of registration order.
type RouterGroup struct {
	engine   *Engine
	parent   *RouterGroup
	prefix   string
	handlers []HandlerFunc
}

func checkHandlers(h []HandlerFunc) {
	if len(h) == 0 {
		panic("gola: at least one handler is required")
	}
	for _, f := range h {
		if f == nil {
			panic("gola: nil handler")
		}
	}
}
func joinPaths(prefix, path string) string {
	if path == "" {
		if prefix == "" {
			return "/"
		}
		return prefix
	}
	if !strings.HasPrefix(path, "/") {
		panic("gola: route or group path must start with /")
	}
	if prefix == "" {
		return path
	}
	return strings.TrimSuffix(prefix, "/") + path
}
func (g *RouterGroup) chain() []HandlerFunc {
	var h []HandlerFunc
	if g.parent != nil {
		h = g.parent.chain()
	}
	return append(h, g.handlers...)
}
func (g *RouterGroup) Use(h ...HandlerFunc) {
	g.engine.assertMutable()
	checkHandlers(h)
	g.handlers = append(g.handlers, h...)
}
func (g *RouterGroup) Group(prefix string, h ...HandlerFunc) *RouterGroup {
	g.engine.assertMutable()
	if len(h) > 0 {
		checkHandlers(h)
	}
	path := joinPaths(g.prefix, prefix)
	parseRoute(path)
	if strings.Contains(path, "*") {
		panic("gola: group cannot contain a wildcard")
	}
	return &RouterGroup{engine: g.engine, parent: g, prefix: path, handlers: append([]HandlerFunc(nil), h...)}
}
func (g *RouterGroup) Handle(method, path string, h ...HandlerFunc) {
	g.engine.addRoute(method, joinPaths(g.prefix, path), g, h)
}
func (g *RouterGroup) GET(path string, h ...HandlerFunc)     { g.Handle("GET", path, h...) }
func (g *RouterGroup) POST(path string, h ...HandlerFunc)    { g.Handle("POST", path, h...) }
func (g *RouterGroup) PUT(path string, h ...HandlerFunc)     { g.Handle("PUT", path, h...) }
func (g *RouterGroup) PATCH(path string, h ...HandlerFunc)   { g.Handle("PATCH", path, h...) }
func (g *RouterGroup) DELETE(path string, h ...HandlerFunc)  { g.Handle("DELETE", path, h...) }
func (g *RouterGroup) HEAD(path string, h ...HandlerFunc)    { g.Handle("HEAD", path, h...) }
func (g *RouterGroup) OPTIONS(path string, h ...HandlerFunc) { g.Handle("OPTIONS", path, h...) }
