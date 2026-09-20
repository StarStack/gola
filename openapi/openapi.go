// Package openapi builds a static OpenAPI document from registered GoLa routes
// and explicit application metadata. It does not infer schemas or permissions.
package openapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/starstack/gola"
)

type Config struct {
	Title       string
	Version     string
	Description string
	// Keys use the registered method and route, for example "GET /users/:id".
	Operations map[string]Operation
}

type Operation struct {
	OperationID string              `json:"operationId,omitempty"`
	Summary     string              `json:"summary,omitempty"`
	Description string              `json:"description,omitempty"`
	Tags        []string            `json:"tags,omitempty"`
	Parameters  []Parameter         `json:"parameters,omitempty"`
	RequestBody *RequestBody        `json:"requestBody,omitempty"`
	Responses   map[string]Response `json:"responses"`
}

type Parameter struct {
	Name        string         `json:"name"`
	In          string         `json:"in"`
	Description string         `json:"description,omitempty"`
	Required    bool           `json:"required,omitempty"`
	Schema      map[string]any `json:"schema"`
	CatchAll    bool           `json:"x-gola-catch-all,omitempty"`
}

type MediaType struct {
	Schema map[string]any `json:"schema,omitempty"`
}

type RequestBody struct {
	Description string               `json:"description,omitempty"`
	Required    bool                 `json:"required,omitempty"`
	Content     map[string]MediaType `json:"content"`
}

type Response struct {
	Description string               `json:"description"`
	Content     map[string]MediaType `json:"content,omitempty"`
}

// Document is an immutable, concurrent-safe HTTP handler supporting GET, HEAD,
// ETag revalidation, and the standard library's conditional request semantics.
type Document struct {
	data []byte
	etag string
}

func New(routes []gola.RouteInfo, config Config) (*Document, error) {
	if strings.TrimSpace(config.Title) == "" || strings.TrimSpace(config.Version) == "" {
		return nil, fmt.Errorf("openapi: title and version are required")
	}
	paths := make(map[string]map[string]Operation)
	shapes := make(map[string]string)
	ids := make(map[string]bool)
	used := make(map[string]bool)
	for _, route := range routes {
		// HTTP method tokens are case-sensitive. A custom "get" route must
		// never be advertised as a standard GET operation.
		switch route.Method {
		case "GET", "PUT", "POST", "DELETE", "OPTIONS", "HEAD", "PATCH", "TRACE":
		default:
			return nil, fmt.Errorf("openapi: unsupported method %q", route.Method)
		}
		method := strings.ToLower(route.Method)
		path, shape, generated, err := convertPath(route.Path)
		if err != nil {
			return nil, err
		}
		if previous, ok := shapes[shape]; ok && previous != path {
			return nil, fmt.Errorf("openapi: ambiguous parameter names in %q and %q", previous, path)
		}
		shapes[shape] = path
		if paths[path] == nil {
			paths[path] = make(map[string]Operation)
		}
		if _, ok := paths[path][method]; ok {
			return nil, fmt.Errorf("openapi: duplicate operation %s %s", method, path)
		}
		key := route.Method + " " + route.Path
		operation := config.Operations[key]
		used[key] = true
		if operation.OperationID != "" {
			if ids[operation.OperationID] {
				return nil, fmt.Errorf("openapi: duplicate operation ID %q", operation.OperationID)
			}
			ids[operation.OperationID] = true
		}
		// Copy the slice before filling inferred path parameters.
		operation.Parameters = append([]Parameter(nil), operation.Parameters...)
		parameters := make(map[string]bool)
		for _, parameter := range operation.Parameters {
			if parameter.Name == "" || parameter.Schema == nil {
				return nil, fmt.Errorf("openapi: parameter name and schema are required for %s", key)
			}
			switch parameter.In {
			case "query", "header", "cookie":
			case "path":
				if !parameter.Required || !hasParameter(generated, parameter.Name) {
					return nil, fmt.Errorf("openapi: invalid path parameter %q for %s", parameter.Name, key)
				}
			default:
				return nil, fmt.Errorf("openapi: invalid parameter location %q", parameter.In)
			}
			identity := parameter.In + ":" + parameter.Name
			if parameter.In == "header" {
				identity = strings.ToLower(identity)
			}
			if parameters[identity] {
				return nil, fmt.Errorf("openapi: duplicate parameter %q", parameter.Name)
			}
			parameters[identity] = true
		}
		for _, parameter := range generated {
			if !parameters["path:"+parameter.Name] {
				operation.Parameters = append(operation.Parameters, parameter)
			}
		}
		if len(operation.Responses) == 0 {
			operation.Responses = map[string]Response{"default": {Description: "Application-defined response"}}
		}
		for code, response := range operation.Responses {
			if !validResponseCode(code) || strings.TrimSpace(response.Description) == "" {
				return nil, fmt.Errorf("openapi: invalid response %q for %s", code, key)
			}
		}
		if operation.RequestBody != nil && len(operation.RequestBody.Content) == 0 {
			return nil, fmt.Errorf("openapi: request body content is required for %s", key)
		}
		paths[path][method] = operation
	}
	for key := range config.Operations {
		if !used[key] {
			return nil, fmt.Errorf("openapi: metadata references unregistered operation %q", key)
		}
	}
	data, err := json.MarshalIndent(map[string]any{
		"openapi": "3.1.1",
		"info":    map[string]string{"title": config.Title, "version": config.Version, "description": config.Description},
		"paths":   paths,
	}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("openapi: encode document: %w", err)
	}
	data = append(data, '\n')
	digest := sha256.Sum256(data)
	return &Document{data: data, etag: `"` + hex.EncodeToString(digest[:]) + `"`}, nil
}

func hasParameter(parameters []Parameter, name string) bool {
	for _, parameter := range parameters {
		if parameter.Name == name {
			return true
		}
	}
	return false
}

func validResponseCode(code string) bool {
	if code == "default" {
		return true
	}
	if len(code) != 3 || code[0] < '1' || code[0] > '5' {
		return false
	}
	return code[1:] == "XX" || code[1] >= '0' && code[1] <= '9' && code[2] >= '0' && code[2] <= '9'
}

func convertPath(path string) (string, string, []Parameter, error) {
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "{}?#\r\n\x00") {
		return "", "", nil, fmt.Errorf("openapi: unrepresentable route %q", path)
	}
	segments := strings.Split(path, "/")
	shape := append([]string(nil), segments...)
	var parameters []Parameter
	for i, segment := range segments {
		if strings.HasPrefix(segment, ":") || strings.HasPrefix(segment, "*") {
			name := segment[1:]
			if name == "" || hasParameter(parameters, name) || segment[0] == '*' && i != len(segments)-1 {
				return "", "", nil, fmt.Errorf("openapi: invalid parameter in %q", path)
			}
			segments[i], shape[i] = "{"+name+"}", "{}"
			parameter := Parameter{Name: name, In: "path", Required: true, Schema: map[string]any{"type": "string"}, CatchAll: segment[0] == '*'}
			if parameter.CatchAll {
				parameter.Description = "Remaining path segments, possibly empty; no leading slash."
			}
			parameters = append(parameters, parameter)
		}
	}
	return strings.Join(segments, "/"), strings.Join(shape, "/"), parameters, nil
}

// Bytes returns a copy so callers cannot mutate the served document.
func (d *Document) Bytes() []byte { return append([]byte(nil), d.data...) }

func (d *Document) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("ETag", d.etag)
	http.ServeContent(w, r, "openapi.json", time.Time{}, bytes.NewReader(d.data))
}
