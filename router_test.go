package gola_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"

	gola "github.com/starstack/gola"
)

func request(e http.Handler, method, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(method, path, nil))
	return w
}
func mustPanic(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Error("expected configuration panic")
		}
	}()
	fn()
}
func TestRouterMatchingAndBacktracking(t *testing.T) {
	routes := []string{"/users/:id", "/users/new", "/files/fixed/end", "/files/:name/other", "/files/*path", "/unicode/:value", "/branch/:id/a", "/branch/:name/b", "/case", "/case/", "/raw//x", "/raw/../x"}
	for _, reverse := range []bool{false, true} {
		e := gola.New()
		for i := range routes {
			n := i
			if reverse {
				n = len(routes) - i - 1
			}
			path := routes[n]
			e.GET(path, func(c *gola.Context) {
				_ = c.String(200, "%s|%s|%s|%s|%s", c.FullPath(), c.Param("id"), c.Param("name"), c.Param("path"), c.Param("value"))
			})
		}
		tests := map[string]string{"/users/new": "/users/new||||", "/users/17": "/users/:id|17|||", "/files/fixed/other": "/files/:name/other||fixed||", "/files/a/b": "/files/*path|||a/b|", "/files/": "/files/*path||||", "/unicode/%252F": "/unicode/:value||||%2F", "/unicode/%E4%BD%A0": "/unicode/:value||||你", "/branch/9/a": "/branch/:id/a|9|||", "/branch/7/b": "/branch/:name/b||7||", "/raw//x": "/raw//x||||", "/raw/../x": "/raw/../x||||"}
		for path, want := range tests {
			w := request(e, "GET", path)
			if w.Code != 200 || w.Body.String() != want {
				t.Errorf("reverse=%v %s: %d %q want %q", reverse, path, w.Code, w.Body.String(), want)
			}
		}
		for _, path := range []string{"/files", "/users/", "/unicode/%2F", "/Case", "/raw/x"} {
			if w := request(e, "GET", path); w.Code != 404 {
				t.Errorf("%s: %d", path, w.Code)
			}
		}
		if _, err := http.NewRequest("GET", "http://example.com/%zz", nil); err == nil {
			t.Fatal("invalid percent encoding accepted")
		}
	}
}
func TestRegistrationValidation(t *testing.T) {
	noop := func(*gola.Context) {}
	for _, path := range []string{"bad", "/:", "/*", "/a/*x/b", "/:x/:x", "/a?query", "/a#frag", "/pre:id"} {
		t.Run(path, func(t *testing.T) { mustPanic(t, func() { gola.New().GET(path, noop) }) })
	}
	for _, paths := range [][2]string{{"/a", "/a"}, {"/:id", "/:name"}, {"/*x", "/*y"}} {
		mustPanic(t, func() { e := gola.New(); e.GET(paths[0], noop); e.GET(paths[1], noop) })
	}
	mustPanic(t, func() { gola.New().GET("/") })
	mustPanic(t, func() { gola.New().GET("/", nil) })
	mustPanic(t, func() { gola.New().Handle("BAD METHOD", "/", noop) })
}
func TestMethodsAndFallbacks(t *testing.T) {
	e := gola.New()
	global := 0
	group := 0
	e.Use(func(c *gola.Context) { global++ })
	g := e.Group("/api", func(c *gola.Context) { group++ })
	g.GET("/x", func(c *gola.Context) { c.Status(201) })
	g.POST("/x", func(c *gola.Context) { c.Status(202) })
	if w := request(e, "HEAD", "/api/x"); w.Code != 405 || w.Header().Get("Allow") != "GET, POST" || w.Body.Len() != 0 {
		t.Fatalf("HEAD: %#v", w)
	}
	if w := request(e, "OPTIONS", "/api/x"); w.Code != 405 {
		t.Fatal(w.Code)
	}
	if w := request(e, "GET", "/missing"); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if group != 0 || global != 3 {
		t.Fatal(group, global)
	}
	e2 := gola.New()
	e2.HEAD("/", func(c *gola.Context) { _ = c.String(200, "hidden") })
	e2.OPTIONS("/", func(c *gola.Context) { c.Status(204) })
	if w := request(e2, "HEAD", "/"); w.Body.Len() != 0 || w.Code != 200 {
		t.Fatal(w)
	}
	if w := request(e2, "OPTIONS", "/"); w.Code != 204 {
		t.Fatal(w)
	}
	e3 := gola.New()
	e3.NoRoute(func(c *gola.Context) {
		if c.FullPath() != "" {
			t.Error(c.FullPath())
		}
		_ = c.String(404, "custom")
	})
	e3.GET("/x", func(*gola.Context) {})
	e3.NoMethod(func(c *gola.Context) {
		if c.FullPath() != "" {
			t.Error(c.FullPath())
		}
		_ = c.String(405, "method")
	})
	if request(e3, "GET", "/missing").Body.String() != "custom" || request(e3, "PUT", "/x").Body.String() != "method" {
		t.Fatal("custom fallback")
	}
}
func TestGroupsLateUseFreezeAndRouteCopies(t *testing.T) {
	e := gola.New()
	var got []string
	g := e.Group("/api")
	sub := g.Group("/v1")
	sub.GET("/x", func(*gola.Context) { got = append(got, "handler") })
	e.GET("/outside", func(*gola.Context) { got = append(got, "outside") })
	g.Use(func(*gola.Context) { got = append(got, "group") })
	e.Use(func(*gola.Context) { got = append(got, "global") })
	routes := e.Routes()
	routes[0].Path = "changed"
	if e.Routes()[0].Path != "/api/v1/x" {
		t.Fatal("routes exposes internal state")
	}
	request(e, "GET", "/api/v1/x")
	request(e, "GET", "/outside")
	if !reflect.DeepEqual(got, []string{"global", "group", "handler", "global", "outside"}) {
		t.Fatal(got)
	}
	for _, fn := range []func(){func() { e.GET("/new", func(*gola.Context) {}) }, func() { g.Use(func(*gola.Context) {}) }, func() { e.Group("/new") }, func() { e.NoRoute(func(*gola.Context) {}) }, func() { gola.WithBodyLimit(9)(e) }} {
		mustPanic(t, fn)
	}
}
func TestMiddlewareExecutionContract(t *testing.T) {
	for _, mode := range []string{"nested", "automatic", "abortA", "abortB", "repeat"} {
		t.Run(mode, func(t *testing.T) {
			var got []string
			e := gola.New()
			e.Use(func(c *gola.Context) {
				got = append(got, "A+")
				if mode == "abortA" {
					c.Abort()
					c.Abort()
					c.Next()
					return
				}
				if mode != "automatic" {
					c.Next()
					if mode == "repeat" {
						c.Next()
					}
					got = append(got, "A-")
				}
			})
			e.Use(func(c *gola.Context) {
				got = append(got, "B+")
				if mode == "abortB" {
					c.Abort()
					return
				}
				c.Next()
				c.Next()
				got = append(got, "B-")
			})
			e.GET("/", func(*gola.Context) { got = append(got, "H") })
			request(e, "GET", "/")
			wants := map[string][]string{"nested": {"A+", "B+", "H", "B-", "A-"}, "automatic": {"A+", "B+", "H", "B-"}, "abortA": {"A+"}, "abortB": {"A+", "B+", "A-"}, "repeat": {"A+", "B+", "H", "B-", "A-"}}
			if !reflect.DeepEqual(got, wants[mode]) {
				t.Fatal(got)
			}
		})
	}
}
func TestWrapAndQuery(t *testing.T) {
	e := gola.New()
	e.GET("/:id", gola.WrapF(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, r.PathValue("id")) }), func(*gola.Context) { t.Error("WrapF failed to stop chain") })
	if got := request(e, "GET", "/42").Body.String(); got != "42" {
		t.Fatal(got)
	}
	e2 := gola.New()
	e2.GET("/", func(c *gola.Context) {
		if v, ok := c.GetQuery("empty"); v != "" || !ok {
			t.Error(v, ok)
		}
		if _, ok := c.GetQuery("missing"); ok {
			t.Error("missing")
		}
		if c.Query("v") != "1" || c.DefaultQuery("empty", "fallback") != "" || c.DefaultQuery("missing", "fallback") != "fallback" {
			t.Error("query contract")
		}
	})
	request(e2, "GET", "/?empty=&v=1&v=2")
}
func TestConcurrentRequestIsolation(t *testing.T) {
	e := gola.New()
	e.Use(gola.RequestID())
	const n = 64
	ready := make(chan struct{}, n)
	release := make(chan struct{})
	ids := sync.Map{}
	e.GET("/:tenant/:user", func(c *gola.Context) {
		c.Set("tenant", c.Param("tenant"))
		c.Set("user", c.Param("user"))
		id := c.Writer.Header().Get("X-Request-ID")
		if _, loaded := ids.LoadOrStore(id, true); loaded || id == "" {
			t.Error("duplicate/empty ID")
		}
		ready <- struct{}{}
		<-release
		tenant, _ := c.Get("tenant")
		user, _ := c.Get("user")
		_ = c.String(200, "%s/%s/%s/%s", tenant, user, c.Param("tenant"), c.Request.PathValue("user"))
	})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			path := fmt.Sprintf("/t%d/u%d", i, i)
			w := request(e, "GET", path)
			want := fmt.Sprintf("t%d/u%d/t%d/u%d", i, i, i, i)
			if w.Body.String() != want {
				t.Error(w.Body.String(), want)
			}
		}()
	}
	for range n {
		<-ready
	}
	close(release)
	wg.Wait()
	other := gola.New()
	other.GET("/", func(c *gola.Context) {
		if _, ok := c.Get("tenant"); ok {
			t.Error("engine leak")
		}
	})
	if w := request(other, "GET", "/"); w.Header().Get("X-Request-ID") != "" {
		t.Error("middleware engine leak")
	}
}
func FuzzRouterPath(f *testing.F) {
	for _, s := range []string{"/a/new", "/a/%2F", "/a/%252F", "/a//", "/a/../x", "/你好", "/%zz", ""} {
		f.Add(s)
	}
	e := gola.New()
	for _, p := range []string{"/a/new", "/a/:id", "/a/*rest", "/你好"} {
		e.GET(p, func(c *gola.Context) {
			if c.Param("id") != c.Request.PathValue("id") {
				panic("path value mismatch")
			}
			c.Status(204)
		})
	}
	f.Fuzz(func(t *testing.T, path string) {
		if len(path) > 8192 {
			return
		}
		u, err := url.ParseRequestURI(path)
		if err != nil {
			return
		}
		r := &http.Request{Method: "GET", URL: u, Header: make(http.Header)}
		w := httptest.NewRecorder()
		e.ServeHTTP(w, r)
		if w.Code != 204 && w.Code != 404 {
			t.Fatalf("status %d for %q", w.Code, path)
		}
	})
}
func TestNoAutomaticNormalization(t *testing.T) {
	e := gola.New()
	e.GET("/x/", func(c *gola.Context) { c.Status(204) })
	w := request(e, "GET", "/x")
	if w.Code != 404 || strings.Contains(w.Header().Get("Location"), "x") {
		t.Fatal(w)
	}
}
