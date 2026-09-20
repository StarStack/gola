package main

import (
	"encoding/xml"
	"errors"
	"html/template"
	"log"
	"net/http"

	"github.com/starstack/gola"
)

type greeting struct {
	XMLName xml.Name `xml:"greeting"`
	Name    string   `xml:"name"`
}

func newRouter() *gola.Engine {
	// Template source belongs to the application; only values come from requests.
	templates := template.Must(template.New("hello").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>GoLa rendering</title></head>
<body><h1>Hello {{.Name}}</h1></body></html>`))
	r := gola.Default(gola.WithHTMLTemplates(templates), gola.WithBodyLimit(64<<10))
	r.GET("/hello", func(c *gola.Context) {
		if err := c.HTML(http.StatusOK, "hello", greeting{Name: c.DefaultQuery("name", "Danny")}); err != nil {
			c.Fail(err)
		}
	})
	r.POST("/xml", func(c *gola.Context) {
		var input greeting
		if err := c.ShouldBindXML(&input); err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, gola.ErrBodyTooLarge) {
				status = http.StatusRequestEntityTooLarge
			} else if errors.Is(err, gola.ErrUnsupportedMediaType) {
				status = http.StatusUnsupportedMediaType
			}
			if err := c.AbortWithStatusJSON(status, gola.H{"error": "invalid XML request"}); err != nil {
				c.Error(err)
			}
			return
		}
		if err := c.XML(http.StatusOK, input); err != nil {
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
