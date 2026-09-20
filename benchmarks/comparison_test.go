package benchmarks_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/starstack/gola"
)

type scenario struct {
	name, path          string
	routes, middlewares int
}

var scenarios = []scenario{
	{"Static", "/hello", 0, 0}, {"Param", "/users/42", 0, 0},
	{"NotFound", "/missing", 0, 0}, {"JSON", "/json", 0, 0},
	{"Middleware5", "/hello", 0, 5},
	{"Routes10", "/routes/r9", 10, 0}, {"Routes100", "/routes/r99", 100, 0},
	{"Routes1000", "/routes/r999", 1000, 0},
}

type payload struct {
	Message string `json:"message"`
}

const textType = "text/plain; charset=utf-8"

func init() { gin.SetMode(gin.ReleaseMode) }

func golaHandler(s scenario) http.Handler {
	r := gola.New()
	for i := range s.middlewares {
		key := fmt.Sprintf("X-Middleware-%d", i)
		r.Use(func(c *gola.Context) { c.Header(key, "active"); c.Next() })
	}
	if s.routes == 0 {
		r.GET("/hello", func(c *gola.Context) {
			if err := c.String(200, "hello"); err != nil {
				panic(err)
			}
		})
		r.GET("/users/:id", func(c *gola.Context) {
			if err := c.String(200, "%s", c.Param("id")); err != nil {
				panic(err)
			}
		})
		r.GET("/json", func(c *gola.Context) {
			if err := c.JSON(200, payload{"hello"}); err != nil {
				panic(err)
			}
		})
	}
	r.NoRoute(func(c *gola.Context) {
		if err := c.String(404, "not found"); err != nil {
			panic(err)
		}
	})
	for i := range s.routes {
		r.GET(fmt.Sprintf("/routes/r%d", i), func(c *gola.Context) {
			if err := c.String(200, "hello"); err != nil {
				panic(err)
			}
		})
	}
	return r
}

func ginHandler(s scenario) http.Handler {
	r := gin.New()
	r.RedirectTrailingSlash, r.RedirectFixedPath = false, false
	r.HandleMethodNotAllowed = true
	if err := r.SetTrustedProxies(nil); err != nil {
		panic(err)
	}
	for i := range s.middlewares {
		key := fmt.Sprintf("X-Middleware-%d", i)
		r.Use(func(c *gin.Context) { c.Header(key, "active"); c.Next() })
	}
	if s.routes == 0 {
		r.GET("/hello", func(c *gin.Context) { c.String(200, "hello") })
		r.GET("/users/:id", func(c *gin.Context) { c.String(200, "%s", c.Param("id")) })
		r.GET("/json", func(c *gin.Context) { c.JSON(200, payload{"hello"}) })
	}
	r.NoRoute(func(c *gin.Context) { c.String(404, "not found") })
	for i := range s.routes {
		r.GET(fmt.Sprintf("/routes/r%d", i), func(c *gin.Context) { c.String(200, "hello") })
	}
	return r
}

func httpHandler(s scenario) http.Handler {
	r := http.NewServeMux()
	write := func(w http.ResponseWriter, status int, contentType string, body []byte) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		if _, err := w.Write(body); err != nil {
			panic(err)
		}
	}
	if s.routes == 0 {
		r.HandleFunc("GET /hello", func(w http.ResponseWriter, _ *http.Request) { write(w, 200, textType, []byte("hello")) })
		r.HandleFunc("GET /users/{id}", func(w http.ResponseWriter, req *http.Request) { write(w, 200, textType, []byte(req.PathValue("id"))) })
		r.HandleFunc("GET /json", func(w http.ResponseWriter, _ *http.Request) {
			body, err := json.Marshal(payload{"hello"})
			if err != nil {
				panic(err)
			}
			write(w, 200, "application/json; charset=utf-8", body)
		})
	}
	r.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { write(w, 404, textType, []byte("not found")) })
	for i := range s.routes {
		r.HandleFunc(fmt.Sprintf("GET /routes/r%d", i), func(w http.ResponseWriter, _ *http.Request) { write(w, 200, textType, []byte("hello")) })
	}
	var handler http.Handler = r
	for i := s.middlewares - 1; i >= 0; i-- {
		key, next := fmt.Sprintf("X-Middleware-%d", i), handler
		handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { w.Header().Set(key, "active"); next.ServeHTTP(w, req) })
	}
	return handler
}

var frameworks = []struct {
	name  string
	build func(scenario) http.Handler
}{
	{"GoLa", golaHandler}, {"Gin", ginHandler}, {"NetHTTP", httpHandler},
}

func TestEquivalentResponses(t *testing.T) {
	for _, s := range scenarios {
		t.Run(s.name, func(t *testing.T) {
			var baseline *httptest.ResponseRecorder
			for _, f := range frameworks {
				w := httptest.NewRecorder()
				f.build(s).ServeHTTP(w, httptest.NewRequest(http.MethodGet, s.path, nil))
				if baseline == nil {
					baseline = w
					continue
				}
				if w.Code != baseline.Code || w.Body.String() != baseline.Body.String() || !reflect.DeepEqual(w.Header(), baseline.Header()) {
					t.Fatalf("%s response differs: status=%d headers=%v body=%q; GoLa=%d %v %q", f.name, w.Code, w.Header(), w.Body.String(), baseline.Code, baseline.Header(), baseline.Body.String())
				}
			}
		})
	}
}

func BenchmarkComparison(b *testing.B) {
	for _, s := range scenarios {
		b.Run(s.name, func(b *testing.B) {
			for _, f := range frameworks {
				b.Run(f.name, func(b *testing.B) {
					h := f.build(s)
					req := httptest.NewRequest(http.MethodGet, s.path, nil)
					h.ServeHTTP(httptest.NewRecorder(), req) // Freeze/initialize before measurement.
					b.ReportAllocs()
					b.ResetTimer()
					for b.Loop() {
						h.ServeHTTP(httptest.NewRecorder(), req)
					}
				})
			}
		})
	}
}
