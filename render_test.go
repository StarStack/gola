package gola_test

import (
	"encoding/xml"
	"errors"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/starstack/gola"
)

func TestHTMLTemplatesEscapeAndClone(t *testing.T) {
	templates := template.Must(template.New("page").Parse(`<a href="{{.URL}}">{{.Text}}</a>`))
	e := gola.New(gola.WithHTMLTemplates(templates))
	e.GET("/", func(c *gola.Context) {
		if err := c.HTML(http.StatusCreated, "page", gola.H{"URL": "javascript:alert(1)", "Text": "<script>"}); err != nil {
			t.Error(err)
		}
	})
	// Parsing a new application template does not replace the engine's clone.
	template.Must(templates.Parse(`replacement`))
	var calls sync.WaitGroup
	for range 10 {
		calls.Go(func() {
			w := request(e, "GET", "/")
			if w.Code != http.StatusCreated || w.Header().Get("Content-Type") != "text/html; charset=utf-8" || w.Body.String() != `<a href="#ZgotmplZ">&lt;script&gt;</a>` {
				t.Errorf("unexpected HTML response: %d %s", w.Code, w.Body.String())
			}
		})
	}
	calls.Wait()
	mustPanic(t, func() { gola.WithHTMLTemplates(templates)(e) })
	mustPanic(t, func() { gola.New(gola.WithHTMLTemplates(nil)) })
	if err := templates.Execute(io.Discard, nil); err != nil {
		t.Fatal(err)
	}
	mustPanic(t, func() { gola.New(gola.WithHTMLTemplates(templates)) })
}

func TestHTMLRenderingErrorsDoNotCommit(t *testing.T) {
	templates := template.Must(template.New("page").Funcs(template.FuncMap{
		"fail": func() (string, error) { return "", errors.New("render failed") },
	}).Parse(`partial{{fail}}`))
	e := gola.New(gola.WithHTMLTemplates(templates))
	e.GET("/", func(c *gola.Context) {
		for _, name := range []string{"page", "missing"} {
			if err := c.HTML(200, name, nil); err == nil || c.Writer.Written() || c.Writer.Size() != 0 || c.Writer.Header().Get("Content-Type") != "" {
				t.Errorf("render failure committed response: %v", err)
			}
		}
		_ = c.String(500, "fallback")
		if err := c.HTML(200, "page", nil); !errors.Is(err, gola.ErrResponseCommitted) {
			t.Error(err)
		}
	})
	if w := request(e, "GET", "/"); w.Body.String() != "fallback" {
		t.Fatal(w.Body.String())
	}
	noTemplates := gola.New()
	noTemplates.GET("/", func(c *gola.Context) {
		if err := c.HTML(200, "page", nil); !errors.Is(err, gola.ErrHTMLTemplatesNotConfigured) || c.Writer.Written() {
			t.Fatal(err)
		}
	})
	request(noTemplates, "GET", "/")
}

type renderXMLValue struct {
	XMLName xml.Name `xml:"message"`
	Text    string   `xml:"text"`
	Count   int      `xml:"count,omitempty"`
}

func TestXMLResponseEncoding(t *testing.T) {
	e := gola.New()
	e.GET("/", func(c *gola.Context) {
		if err := c.XML(200, make(chan int)); err == nil || c.Writer.Written() {
			t.Fatal("XML encoding error committed response", err)
		}
		if err := c.XML(201, renderXMLValue{Text: "<script>&"}); err != nil {
			t.Fatal(err)
		}
		if err := c.XML(200, nil); !errors.Is(err, gola.ErrResponseCommitted) {
			t.Fatal(err)
		}
	})
	w := request(e, "GET", "/")
	if w.Code != 201 || w.Header().Get("Content-Type") != "application/xml; charset=utf-8" || w.Body.String() != "<message><text>&lt;script&gt;&amp;</text></message>" {
		t.Fatal(w)
	}
	for _, method := range []string{"GET", "HEAD"} {
		for _, code := range []int{200, 204, 304} {
			r := gola.New(gola.WithHTMLTemplates(template.Must(template.New("page").Parse("value"))))
			r.Handle(method, "/xml", func(c *gola.Context) { _ = c.XML(code, renderXMLValue{}) })
			r.Handle(method, "/html", func(c *gola.Context) { _ = c.HTML(code, "page", nil) })
			for _, path := range []string{"/xml", "/html"} {
				if w := request(r, method, path); (method == "HEAD" || code != 200) && w.Body.Len() != 0 {
					t.Errorf("unexpected body for %s %s %d", method, path, code)
				}
			}
		}
	}
}

func TestXMLBindingDocumentAndMedia(t *testing.T) {
	for _, tt := range []struct {
		name, media, body string
		want              error
	}{
		{"xml", "application/xml", `<message><text>Danny</text></message>`, nil},
		{"UTF-8 BOM", "application/xml", "\xef\xbb\xbf" + `<?xml version="1.0"?><message/>`, nil},
		{"repeated BOM", "application/xml", "\xef\xbb\xbf\xef\xbb\xbf<message/>", gola.ErrBindingSyntax},
		{"trailing BOM", "application/xml", "<message/>\xef\xbb\xbf", gola.ErrBindingSyntax},
		{"text xml", "text/xml; charset=utf-8", `<message/>`, nil},
		{"suffix", "application/vnd.gola+xml", `<message/>`, nil},
		{"declaration and comment", "application/xml", `<?xml version="1.0"?><!--before--><message/><!--after-->`, nil},
		{"escaped entities", "application/xml", `<message><text>&amp;&lt;</text></message>`, nil},
		{"multiple roots", "application/xml", `<message/><message/>`, gola.ErrBindingSyntax},
		{"trailing garbage", "application/xml", `<message/>invalid`, gola.ErrBindingSyntax},
		{"leading garbage", "application/xml", `invalid<message/>`, gola.ErrBindingSyntax},
		{"trailing bad XML", "application/xml", `<message/><`, gola.ErrBindingSyntax},
		{"empty", "application/xml", "", gola.ErrBindingSyntax},
		{"mismatched", "application/xml", `<message></other>`, gola.ErrBindingSyntax},
		{"DOCTYPE", "application/xml", `<!DOCTYPE message><message/>`, gola.ErrBindingSyntax},
		{"external entity", "application/xml", `<!DOCTYPE message [<!ENTITY data SYSTEM "file:///etc/passwd">]><message>&data;</message>`, gola.ErrBindingSyntax},
		{"undefined entity", "application/xml", `<message>&data;</message>`, gola.ErrBindingSyntax},
		{"processing instruction", "application/xml", `<message/><?unsafe action?>`, gola.ErrBindingSyntax},
		{"trailing declaration", "application/xml", `<message/><?xml version="1.0"?>`, gola.ErrBindingSyntax},
		{"numeric conversion", "application/xml", `<message><count>abc</count></message>`, gola.ErrBindingType},
		{"missing media", "", `<message/>`, gola.ErrUnsupportedMediaType},
		{"wrong media", "application/json", `<message/>`, gola.ErrUnsupportedMediaType},
		{"wildcard media", "application/*+xml", `<message/>`, gola.ErrUnsupportedMediaType},
		{"empty subtype", "application/+xml", `<message/>`, gola.ErrUnsupportedMediaType},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := gola.New()
			e.POST("/", func(c *gola.Context) {
				var dst renderXMLValue
				err := c.ShouldBindXML(&dst)
				if !errors.Is(err, tt.want) {
					t.Fatalf("error = %v, want %v", err, tt.want)
				}
				if c.Writer.Written() || c.IsAborted() {
					t.Fatal("binding changed response or middleware chain")
				}
				if tt.want != nil {
					var classified *gola.BindingError
					if !errors.As(err, &classified) {
						t.Fatal("expected BindingError", err)
					}
				}
			})
			req := httptest.NewRequest("POST", "/", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", tt.media)
			e.ServeHTTP(httptest.NewRecorder(), req)
		})
	}
}

func TestXMLBindingLimitsValidationAndTargets(t *testing.T) {
	for _, length := range []int64{100, -1} {
		e := gola.New(gola.WithBodyLimit(16))
		e.POST("/", func(c *gola.Context) {
			var value renderXMLValue
			err := c.ShouldBindXML(&value)
			var limit *http.MaxBytesError
			if !errors.Is(err, gola.ErrBodyTooLarge) || !errors.As(err, &limit) || c.Writer.Written() {
				t.Fatal("body limit not preserved", err)
			}
		})
		req := httptest.NewRequest("POST", "/", strings.NewReader(`<message>`+strings.Repeat("x", 100)+`</message>`))
		req.ContentLength = length
		req.Header.Set("Content-Type", "application/xml")
		e.ServeHTTP(httptest.NewRecorder(), req)
	}
	sentinel := errors.New("validation failed")
	validated := 0
	e := gola.New(gola.WithValidator(bindingValidatorFunc(func(any) error { validated++; return sentinel })))
	e.POST("/", func(c *gola.Context) {
		for _, target := range []any{nil, 1, (*renderXMLValue)(nil), new(string)} {
			if err := c.ShouldBindXML(target); !errors.Is(err, gola.ErrInvalidBindingTarget) {
				t.Fatal(err)
			}
		}
		var value renderXMLValue
		if err := c.ShouldBindXML(&value); !errors.Is(err, gola.ErrValidation) || !errors.Is(err, sentinel) {
			t.Fatal(err)
		}
	})
	req := httptest.NewRequest("POST", "/", strings.NewReader(`<message/>`))
	req.Header.Set("Content-Type", "application/xml")
	e.ServeHTTP(httptest.NewRecorder(), req)
	if validated != 1 {
		t.Fatal("validator calls", validated)
	}
}

func FuzzXMLBinding(f *testing.F) {
	for _, seed := range []string{`<message><count>1</count></message>`, `<!DOCTYPE message><message/>`, `<message/><message/>`, `<message>&missing;</message>`, ""} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body string) {
		e := gola.New(gola.WithBodyLimit(8 << 10))
		e.POST("/", func(c *gola.Context) {
			var target renderXMLValue
			err := c.ShouldBindXML(&target)
			if c.Writer.Written() || c.IsAborted() {
				t.Fatal("binding changed response state")
			}
			if err != nil {
				var classified *gola.BindingError
				if !errors.As(err, &classified) {
					t.Fatal("unclassified XML failure", err)
				}
			}
		})
		req := httptest.NewRequest("POST", "/", strings.NewReader(body))
		req.ContentLength = -1
		req.Header.Set("Content-Type", "application/xml")
		e.ServeHTTP(httptest.NewRecorder(), req)
	})
}
