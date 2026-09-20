// A minimal GoLa service. Run from the repository: go run ./examples/hello
package main

import (
	"log"
	"net/http"

	"github.com/starstack/gola"
)

func newRouter() *gola.Engine {
	r := gola.Default()
	r.GET("/hello/:name", func(c *gola.Context) {
		if err := c.JSON(http.StatusOK, map[string]string{"hello": c.Param("name")}); err != nil {
			c.Fail(err)
		}
	})
	return r
}

func main() {
	if err := newRouter().Run("127.0.0.1:8080"); err != nil {
		log.Fatal(err)
	}
}
