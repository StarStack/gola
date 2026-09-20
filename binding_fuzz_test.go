package gola_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	gola "github.com/starstack/gola"
)

func FuzzBindingValues(f *testing.F) {
	for _, seed := range []string{"v=1&v=2", "v=128", "v=%XX", "v=a;b=c", "v=", "v=%252F", "v=%E6%96%B0%E5%8A%A0%E5%9D%A1"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 4096 {
			t.Skip()
		}
		engine := gola.New(gola.WithBodyLimit(4096))
		engine.POST("/", func(c *gola.Context) {
			var stringsTarget struct {
				V []string `form:"v"`
			}
			parseResult, parseErr := url.ParseQuery(raw)
			bindErr := c.ShouldBindQuery(&stringsTarget)
			if parseErr != nil {
				if !errors.Is(bindErr, gola.ErrBindingSyntax) {
					t.Fatalf("malformed query accepted: %v", bindErr)
				}
			} else if bindErr != nil || !reflect.DeepEqual(stringsTarget.V, parseResult["v"]) {
				t.Fatalf("query order or decoding mismatch: result=%v expected=%v error=%v", stringsTarget.V, parseResult["v"], bindErr)
			}
			var scalar struct {
				V int8 `form:"v"`
			}
			var slice struct {
				V []int `form:"v"`
			}
			var pointer struct {
				V *bool `form:"v"`
			}
			_ = c.ShouldBindQuery(&scalar)
			_ = c.ShouldBindQuery(&slice)
			_ = c.ShouldBindForm(&pointer)
			_ = c.PostForm("v")
			if c.Writer.Written() || c.IsAborted() {
				t.Fatal("binding committed or aborted the request")
			}
		})
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(raw))
		req.URL = &url.URL{Path: "/", RawQuery: raw}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		engine.ServeHTTP(httptest.NewRecorder(), req)
	})
}

func FuzzBindingJSON(f *testing.F) {
	for _, seed := range []string{`{}`, `null`, `{"v":1}`, `{"v":"x"}`, `{} {}`, `{"v":`, "{} \n", `{"v":1e1000}`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 4096 {
			t.Skip()
		}
		engine := gola.New(gola.WithBodyLimit(1024), gola.WithDisallowUnknownFields(true))
		body := &countingBindingBody{reader: strings.NewReader(raw)}
		engine.POST("/", func(c *gola.Context) {
			var dst struct {
				V int `json:"v"`
			}
			err := c.ShouldBindJSON(&dst)
			if err == nil && (!json.Valid([]byte(raw)) || len(raw) > 1024) {
				t.Fatal("accepted invalid, multiple, or oversized JSON")
			}
			if body.read > 1025 || c.Writer.Written() || c.IsAborted() {
				t.Fatal("binding exceeded its read allowance or changed the response")
			}
		})
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.Body = body
		req.ContentLength = -1
		req.Header.Set("Content-Type", "application/json")
		engine.ServeHTTP(httptest.NewRecorder(), req)
	})
}
