package gola

import "net/http"

// Context is request-local and must not be retained or concurrently modified.
// Use Request.Context() for cancellation and typed application values.
type Context struct {
	Request         *http.Request
	Writer          *ResponseWriter
	engine          *Engine
	params          map[string]string
	fullPath        string
	values          map[string]any
	errors          []error
	handlers        []HandlerFunc
	index           int
	aborted         bool
	standardHandler bool
	formBinding     *formBinding
	multipartForm   *multipartBinding
	sseStarted      bool
}

func (c *Context) Next() {
	for !c.aborted && c.index < len(c.handlers) {
		h := c.handlers[c.index]
		c.index++
		h(c)
	}
}
func (c *Context) Abort()                     { c.aborted = true }
func (c *Context) IsAborted() bool            { return c.aborted }
func (c *Context) AbortWithStatus(status int) { c.Abort(); c.Status(status) }
func (c *Context) AbortWithStatusJSON(status int, value any) error {
	c.Abort()
	return c.JSON(status, value)
}
func (c *Context) Param(key string) string { return c.params[key] }
func (c *Context) Query(key string) string { v, _ := c.GetQuery(key); return v }
func (c *Context) GetQuery(key string) (string, bool) {
	values, ok := c.Request.URL.Query()[key]
	if !ok || len(values) == 0 {
		return "", false
	}
	return values[0], true
}
func (c *Context) DefaultQuery(key, fallback string) string {
	if v, ok := c.GetQuery(key); ok {
		return v
	}
	return fallback
}
func (c *Context) GetHeader(key string) string { return c.Request.Header.Get(key) }
func (c *Context) FullPath() string            { return c.fullPath }
func (c *Context) Set(key string, value any) {
	if c.values == nil {
		c.values = make(map[string]any)
	}
	c.values[key] = value
}
func (c *Context) Get(key string) (any, bool) { v, ok := c.values[key]; return v, ok }
func WrapH(handler http.Handler) HandlerFunc {
	if isNil(handler) {
		panic("gola: nil HTTP handler")
	}
	return func(c *Context) {
		c.standardHandler = true
		// Standard handlers own their status, including the implicit 200 when
		// installed as a custom NoRoute/NoMethod endpoint.
		if !c.Writer.Written() {
			c.Writer.status = http.StatusOK
		}
		defer c.Abort()
		handler.ServeHTTP(c.Writer, c.Request)
	}
}
func WrapF(handler http.HandlerFunc) HandlerFunc { return WrapH(handler) }
