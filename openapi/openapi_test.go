package openapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/starstack/gola"
	"github.com/starstack/gola/openapi"
)

func TestRouteDocumentAndImmutableMetadata(t *testing.T) {
	r := gola.New()
	r.GET("/users/:id", func(*gola.Context) {})
	r.POST("/users/:id", func(*gola.Context) {})
	r.GET("/assets/*path", func(*gola.Context) {})
	schema := map[string]any{"type": "integer", "minimum": 1}
	config := openapi.Config{Title: "Example API", Version: "1", Operations: map[string]openapi.Operation{
		"GET /users/:id": {
			OperationID: "getUser", Summary: "Read a user",
			Parameters: []openapi.Parameter{{Name: "id", In: "path", Required: true, Schema: schema}},
			Responses:  map[string]openapi.Response{"200": {Description: "User"}, "404": {Description: "Not found"}},
		},
	}}
	doc, err := openapi.New(r.Routes(), config)
	if err != nil {
		t.Fatal(err)
	}
	original := string(doc.Bytes())
	schema["type"] = "changed"
	config.Operations["GET /users/:id"] = openapi.Operation{Summary: "changed"}
	copy := doc.Bytes()
	copy[0] = '!'
	if string(doc.Bytes()) != original {
		t.Fatal("caller mutated the served document")
	}
	var parsed struct {
		OpenAPI string                                  `json:"openapi"`
		Paths   map[string]map[string]openapi.Operation `json:"paths"`
	}
	if err := json.Unmarshal(doc.Bytes(), &parsed); err != nil {
		t.Fatal(err)
	}
	get := parsed.Paths["/users/{id}"]["get"]
	post := parsed.Paths["/users/{id}"]["post"]
	if parsed.OpenAPI != "3.1.1" || get.OperationID != "getUser" || len(get.Parameters) != 1 || get.Parameters[0].Schema["type"] != "integer" || len(get.Responses) != 2 {
		t.Fatalf("explicit operation metadata lost: %+v", get)
	}
	if len(post.Parameters) != 1 || !post.Parameters[0].Required || post.Responses["default"].Description == "" {
		t.Fatalf("generated parameters/default response incorrect: %+v", post)
	}
	if !parsed.Paths["/assets/{path}"]["get"].Parameters[0].CatchAll {
		t.Fatal("catch-all route was not distinguished from a single segment")
	}
}

func TestDocumentHTTP(t *testing.T) {
	doc, err := openapi.New(nil, openapi.Config{Title: "Empty API", Version: "1"})
	if err != nil {
		t.Fatal(err)
	}
	get := httptest.NewRecorder()
	doc.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/openapi.json", nil))
	if get.Code != http.StatusOK || !json.Valid(get.Body.Bytes()) || get.Header().Get("ETag") == "" || get.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("GET: %d %v %q", get.Code, get.Header(), get.Body.String())
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		request := httptest.NewRequest(method, "/openapi.json", nil)
		request.Header.Set("If-None-Match", get.Header().Get("ETag"))
		w := httptest.NewRecorder()
		doc.ServeHTTP(w, request)
		if w.Code != http.StatusNotModified || w.Body.Len() != 0 {
			t.Fatalf("conditional %s: %d %q", method, w.Code, w.Body.String())
		}
	}
	head := httptest.NewRecorder()
	doc.ServeHTTP(head, httptest.NewRequest(http.MethodHead, "/openapi.json", nil))
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") != get.Header().Get("Content-Length") {
		t.Fatal("HEAD did not match GET headers without a body")
	}
	post := httptest.NewRecorder()
	doc.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/openapi.json", nil))
	if post.Code != http.StatusMethodNotAllowed || post.Header().Get("Allow") != "GET, HEAD" {
		t.Fatal("unsupported method was accepted")
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			w := httptest.NewRecorder()
			doc.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/openapi.json", nil))
			if string(w.Body.Bytes()) != string(doc.Bytes()) {
				t.Error("concurrent response changed")
			}
		})
	}
	wg.Wait()
}

func TestInvalidDocumentConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		routes []gola.RouteInfo
		config openapi.Config
	}{
		{"missing info", nil, openapi.Config{}},
		{"unknown metadata", nil, openapi.Config{Title: "API", Version: "1", Operations: map[string]openapi.Operation{"GET /missing": {}}}},
		{"unsupported method", []gola.RouteInfo{{Method: "CONNECT", Path: "/"}}, openapi.Config{Title: "API", Version: "1"}},
		{"lowercase custom method", []gola.RouteInfo{{Method: "get", Path: "/"}}, openapi.Config{Title: "API", Version: "1"}},
		{"mixed case custom method", []gola.RouteInfo{{Method: "Post", Path: "/"}}, openapi.Config{Title: "API", Version: "1"}},
		{"duplicate", []gola.RouteInfo{{Method: "GET", Path: "/"}, {Method: "GET", Path: "/"}}, openapi.Config{Title: "API", Version: "1"}},
		{"parameter aliases", []gola.RouteInfo{{Method: "GET", Path: "/:id"}, {Method: "POST", Path: "/:name"}}, openapi.Config{Title: "API", Version: "1"}},
		{"literal braces", []gola.RouteInfo{{Method: "GET", Path: "/{literal}"}}, openapi.Config{Title: "API", Version: "1"}},
		{"nonfinal catch-all", []gola.RouteInfo{{Method: "GET", Path: "/files/*rest/item"}}, openapi.Config{Title: "API", Version: "1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := openapi.New(tc.routes, tc.config); err == nil {
				t.Fatal("invalid document configuration accepted")
			}
		})
	}
	for _, operation := range []openapi.Operation{
		{Parameters: []openapi.Parameter{{Name: "missing", In: "path", Required: true, Schema: map[string]any{}}}},
		{Parameters: []openapi.Parameter{{Name: "id", In: "path", Schema: map[string]any{}}}},
		{Parameters: []openapi.Parameter{{Name: "q", In: "body", Schema: map[string]any{}}}},
		{Responses: map[string]openapi.Response{"600": {Description: "invalid"}}},
		{Responses: map[string]openapi.Response{"200": {}}},
		{RequestBody: &openapi.RequestBody{}},
		{Parameters: []openapi.Parameter{{Name: "q", In: "query", Schema: map[string]any{"type": func() {}}}}},
	} {
		_, err := openapi.New([]gola.RouteInfo{{Method: "GET", Path: "/:id"}}, openapi.Config{Title: "API", Version: "1", Operations: map[string]openapi.Operation{"GET /:id": operation}})
		if err == nil || !strings.Contains(err.Error(), "openapi:") {
			t.Fatalf("invalid operation accepted: %+v, %v", operation, err)
		}
	}
}
