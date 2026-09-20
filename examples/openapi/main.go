package main

import (
	"log"
	"net/http"

	"github.com/starstack/gola"
	"github.com/starstack/gola/openapi"
)

func newRouter() (*gola.Engine, error) {
	r := gola.Default()
	r.GET("/hello/:name", func(c *gola.Context) {
		if err := c.JSON(http.StatusOK, gola.H{"hello": c.Param("name")}); err != nil {
			c.Fail(err)
		}
	})
	document, err := openapi.New(r.Routes(), openapi.Config{
		Title: "GoLa example API", Version: gola.Version,
		Operations: map[string]openapi.Operation{
			"GET /hello/:name": {
				OperationID: "greet", Summary: "Greet a named visitor",
				Responses: map[string]openapi.Response{"200": {
					Description: "Greeting",
					Content: map[string]openapi.MediaType{"application/json": {
						Schema: map[string]any{"type": "object", "required": []string{"hello"}, "properties": map[string]any{"hello": map[string]any{"type": "string"}}},
					}},
				}},
			},
		},
	})
	if err != nil {
		return nil, err
	}
	r.GET("/openapi.json", gola.WrapH(document))
	r.HEAD("/openapi.json", gola.WrapH(document))
	return r, nil
}

func main() {
	r, err := newRouter()
	if err != nil {
		log.Fatal(err)
	}
	if err := r.Run("127.0.0.1:8080"); err != nil {
		log.Fatal(err)
	}
}
