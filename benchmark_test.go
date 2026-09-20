package gola_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/starstack/gola"
)

// Root benchmarks exercise the dependency-free module. Cross-framework cases
// and response-equivalence tests live in the independent benchmarks module.
func BenchmarkGoLa(b *testing.B) {
	for _, kind := range []string{"Static", "Param", "NotFound", "JSON", "Middleware"} {
		b.Run(kind, func(b *testing.B) {
			r := gola.New()
			if kind == "Middleware" {
				for range 5 {
					r.Use(func(c *gola.Context) { c.Next() })
				}
			}
			r.GET("/hello", func(c *gola.Context) { c.Error(c.String(http.StatusOK, "hello")) })
			r.GET("/users/:id", func(c *gola.Context) { c.Error(c.String(http.StatusOK, "%s", c.Param("id"))) })
			r.GET("/json", func(c *gola.Context) {
				c.Error(c.JSON(http.StatusOK, struct {
					Message string `json:"message"`
				}{"hello"}))
			})
			path := "/hello"
			switch kind {
			case "Param":
				path = "/users/42"
			case "NotFound":
				path = "/missing"
			case "JSON":
				path = "/json"
			}
			req := httptest.NewRequest(http.MethodGet, path, nil)
			r.ServeHTTP(httptest.NewRecorder(), req)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				r.ServeHTTP(httptest.NewRecorder(), req)
			}
		})
	}
}
