package gola_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"

	gola "github.com/starstack/gola"
)

func TestJSONBindingContract(t *testing.T) {
	tests := []struct {
		name, media, body string
		strict            bool
		wantErr           error
	}{
		{"object", "application/json", `{"name":"Danny"}`, false, nil},
		{"suffix and charset", "application/problem+json; charset=utf-8", `{"name":"Danny"}`, false, nil},
		{"trailing whitespace", "application/json", "{\"name\":\"Danny\"} \n\t", false, nil},
		{"unknown allowed", "application/json", `{"name":"Danny","extra":true}`, false, nil},
		{"unknown rejected", "application/json", `{"extra":true}`, true, gola.ErrBindingSyntax},
		{"empty", "application/json", "", false, gola.ErrBindingSyntax},
		{"whitespace only", "application/json", "  \n", false, gola.ErrBindingSyntax},
		{"malformed", "application/json", `{"name":`, false, gola.ErrBindingSyntax},
		{"wrong type", "application/json", `{"name":42}`, false, gola.ErrBindingType},
		{"two values", "application/json", `{} {}`, false, gola.ErrBindingSyntax},
		{"trailing null", "application/json", `{} null`, false, gola.ErrBindingSyntax},
		{"trailing garbage", "application/json", `{} nope`, false, gola.ErrBindingSyntax},
		{"missing media", "", `{}`, false, gola.ErrUnsupportedMediaType},
		{"unsupported media", "text/plain", `{}`, false, gola.ErrUnsupportedMediaType},
		{"invalid media", "application/json; charset", `{}`, false, gola.ErrUnsupportedMediaType},
		{"invalid suffix", "application/+json", `{}`, false, gola.ErrUnsupportedMediaType},
		{"wildcard media", "application/*+json", `{}`, false, gola.ErrUnsupportedMediaType},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := gola.New(gola.WithDisallowUnknownFields(tt.strict))
			var gotErr error
			continued := false
			engine.POST("/", func(c *gola.Context) {
				var target struct {
					Name string `json:"name"`
				}
				gotErr = c.ShouldBindJSON(&target)
				if c.IsAborted() {
					t.Error("ShouldBindJSON aborted the request")
				}
			}, func(c *gola.Context) {
				continued = true
				c.Status(http.StatusAccepted)
			})
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", tt.media)
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, req)
			if !errors.Is(gotErr, tt.wantErr) {
				t.Fatalf("error = %v, want %v", gotErr, tt.wantErr)
			}
			if !continued || recorder.Code != http.StatusAccepted || recorder.Body.Len() != 0 {
				t.Fatalf("binding changed response/chain: continued=%v status=%d body=%q", continued, recorder.Code, recorder.Body.String())
			}
			if tt.wantErr != nil {
				var bindingErr *gola.BindingError
				if !errors.As(gotErr, &bindingErr) || bindingErr.Kind != tt.wantErr {
					t.Fatalf("expected classified BindingError: %v", gotErr)
				}
			}
		})
	}
}

func TestJSONBindingPreservesUnderlyingErrors(t *testing.T) {
	engine := gola.New()
	var got error
	engine.POST("/", func(c *gola.Context) {
		var dst struct{ Count int }
		got = c.ShouldBindJSON(&dst)
	})
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"Count":"wrong"}`))
	req.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(httptest.NewRecorder(), req)
	var typeErr *json.UnmarshalTypeError
	if !errors.As(got, &typeErr) || typeErr.Field != "Count" {
		t.Fatalf("underlying JSON error lost: %v", got)
	}
}

func TestBindingInvalidTargets(t *testing.T) {
	var nilStruct *struct{ Name string }
	for _, target := range []any{nil, nilStruct, struct{ Name string }{}, 42} {
		engine := gola.New()
		engine.POST("/:name", func(c *gola.Context) {
			for name, bind := range map[string]func(any) error{
				"json": c.ShouldBindJSON, "query": c.ShouldBindQuery,
				"uri": c.ShouldBindURI, "form": c.ShouldBindForm,
			} {
				if err := bind(target); !errors.Is(err, gola.ErrInvalidBindingTarget) {
					t.Errorf("%s binding to %T = %v", name, target, err)
				}
			}
		})
		engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/Danny", nil))
	}
}

func TestBindingSourceIsolationAndScalarTypes(t *testing.T) {
	type token string
	type input struct {
		Name     token    `form:"name" uri:"name"`
		Enabled  bool     `form:"enabled"`
		Count    int8     `form:"count"`
		Unsigned uint16   `form:"unsigned"`
		Ratio    float32  `form:"ratio"`
		Tags     []string `form:"tag"`
		Numbers  []int    `form:"number"`
		Present  *string  `form:"present"`
		Missing  *int     `form:"missing"`
		Ignored  string   `form:"-"`
		Fallback string
		private  string
	}
	engine := gola.New()
	var query, form, uri input
	engine.POST("/:name", func(c *gola.Context) {
		for _, binding := range []struct {
			bind func(any) error
			dst  any
		}{{c.ShouldBindQuery, &query}, {c.ShouldBindURI, &uri}, {c.ShouldBindForm, &form}} {
			if err := binding.bind(binding.dst); err != nil {
				t.Error(err)
			}
		}
		if c.PostForm("name") != "body" || c.PostForm("name") != "body" || c.PostForm("queryOnly") != "" {
			t.Error("PostForm lost cached values or merged query values")
		}
	})
	queryValues := url.Values{
		"name": {"query", "second"}, "enabled": {"true"}, "count": {"-12"},
		"unsigned": {"65535"}, "ratio": {"1.5"}, "tag": {"a", "", "c"},
		"number": {"2", "3"}, "present": {""}, "queryOnly": {"secret"},
		"Ignored": {"ignored"}, "Fallback": {"field-name"}, "private": {"hidden"},
	}
	req := httptest.NewRequest(http.MethodPost, "/path%252Fvalue?"+queryValues.Encode(), strings.NewReader("name=body"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	engine.ServeHTTP(httptest.NewRecorder(), req)
	if query.Name != "query" || !query.Enabled || query.Count != -12 || query.Unsigned != 65535 || query.Ratio != 1.5 {
		t.Fatalf("scalar values = %+v", query)
	}
	if !reflect.DeepEqual(query.Tags, []string{"a", "", "c"}) || !reflect.DeepEqual(query.Numbers, []int{2, 3}) {
		t.Fatalf("slice values = %+v", query)
	}
	if query.Present == nil || *query.Present != "" || query.Missing != nil || query.Fallback != "field-name" || query.private != "" || query.Ignored != "" {
		t.Fatalf("missing/empty/export/tag handling = %+v", query)
	}
	if uri.Name != "path%2Fvalue" || form.Name != "body" || form.Enabled || len(form.Tags) != 0 {
		t.Fatalf("sources merged or URI decoded twice: uri=%+v form=%+v", uri, form)
	}
}

func TestValueBindingRejectsConversionFailures(t *testing.T) {
	tests := []struct {
		query string
		dst   func() any
	}{
		{"v=128", func() any {
			return &struct {
				V int8 `form:"v"`
			}{}
		}},
		{"v=-1", func() any {
			return &struct {
				V uint `form:"v"`
			}{}
		}},
		{"v=wat", func() any {
			return &struct {
				V bool `form:"v"`
			}{}
		}},
		{"v=", func() any {
			return &struct {
				V int `form:"v"`
			}{}
		}},
		{"v=1e100", func() any {
			return &struct {
				V float32 `form:"v"`
			}{}
		}},
		{"v=NaN", func() any {
			return &struct {
				V float64 `form:"v"`
			}{}
		}},
		{"v=Inf", func() any {
			return &struct {
				V float64 `form:"v"`
			}{}
		}},
		{"v=1&v=bad", func() any {
			return &struct {
				V []int `form:"v"`
			}{}
		}},
		{"v=bad", func() any {
			return &struct {
				V *int `form:"v"`
			}{}
		}},
		{"v=x", func() any {
			return &struct {
				V map[string]string `form:"v"`
			}{}
		}},
		{"v=x", func() any {
			return &struct {
				V struct{ Name string } `form:"v"`
			}{}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.query+"/"+reflect.TypeOf(tt.dst()).String(), func(t *testing.T) {
			engine := gola.New()
			engine.GET("/", func(c *gola.Context) {
				err := c.ShouldBindQuery(tt.dst())
				var bindingErr *gola.BindingError
				if !errors.Is(err, gola.ErrBindingType) || !errors.As(err, &bindingErr) || bindingErr.Field != "v" {
					t.Fatalf("conversion error = %v", err)
				}
			})
			engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/?"+tt.query, nil))
		})
	}
}

func TestMalformedValuesAreNotPartiallyBound(t *testing.T) {
	for _, source := range []string{"query", "form"} {
		for _, raw := range []string{"name=Danny&bad=%XX", "name=Danny;admin=true"} {
			engine := gola.New()
			engine.POST("/", func(c *gola.Context) {
				dst := struct {
					Name string `form:"name"`
				}{Name: "original"}
				bind := c.ShouldBindQuery
				if source == "form" {
					bind = c.ShouldBindForm
				}
				if err := bind(&dst); !errors.Is(err, gola.ErrBindingSyntax) || dst.Name != "original" {
					t.Errorf("%s %q: error=%v target=%+v", source, raw, err, dst)
				}
			})
			req := httptest.NewRequest(http.MethodPost, "/?"+raw, strings.NewReader(raw))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			engine.ServeHTTP(httptest.NewRecorder(), req)
		}
	}
}

type bindingValidatorFunc func(any) error

func (f bindingValidatorFunc) Validate(value any) error { return f(value) }

func TestValidatorRunsAfterSuccessfulBindingOnly(t *testing.T) {
	failure := errors.New("application field validation")
	for _, source := range []string{"json", "query", "uri", "form"} {
		t.Run(source, func(t *testing.T) {
			calls := 0
			engine := gola.New(gola.WithValidator(bindingValidatorFunc(func(value any) error {
				calls++
				return failure
			})))
			engine.POST("/:v", func(c *gola.Context) {
				var dst struct {
					V int `json:"v" form:"v" uri:"v"`
				}
				bind := map[string]func(any) error{
					"json": c.ShouldBindJSON, "query": c.ShouldBindQuery,
					"uri": c.ShouldBindURI, "form": c.ShouldBindForm,
				}[source]
				err := bind(&dst)
				if c.Param("v") == "5" {
					if !errors.Is(err, gola.ErrValidation) || !errors.Is(err, failure) || dst.V != 5 {
						t.Errorf("validation result = %v, dst = %+v", err, dst)
					}
				} else if !errors.Is(err, gola.ErrBindingType) {
					t.Errorf("conversion result = %v", err)
				}
			})
			for _, value := range []string{"5", "wrong"} {
				body, media := "v="+value, "application/x-www-form-urlencoded"
				if source == "json" {
					body, media = `{"v":`+value+`}`, "application/json"
					if value == "wrong" {
						body = `{"v":"wrong"}`
					}
				}
				req := httptest.NewRequest(http.MethodPost, "/"+value+"?v="+value, strings.NewReader(body))
				req.Header.Set("Content-Type", media)
				engine.ServeHTTP(httptest.NewRecorder(), req)
			}
			if calls != 1 {
				t.Fatalf("validator calls = %d, want 1", calls)
			}
		})
	}
}

type countingBindingBody struct {
	reader io.Reader
	read   int
}

func (b *countingBindingBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	b.read += n
	return n, err
}
func (*countingBindingBody) Close() error { return nil }

func TestBindingBodyLimitsKnownAndUnknownLength(t *testing.T) {
	for _, source := range []string{"json", "form"} {
		for _, length := range []int64{-1, 0, 1000} {
			t.Run(source+"/length="+strconv.FormatInt(length, 10), func(t *testing.T) {
				const limit = 32
				engine := gola.New(gola.WithBodyLimit(limit))
				var got error
				engine.POST("/", func(c *gola.Context) {
					var dst struct {
						V string `json:"v" form:"v"`
					}
					if source == "json" {
						got = c.ShouldBindJSON(&dst)
					} else {
						got = c.ShouldBindForm(&dst)
					}
					c.Status(http.StatusTeapot)
				})
				payload, media := "v="+strings.Repeat("a", 100), "application/x-www-form-urlencoded"
				if source == "json" {
					payload, media = `{"v":"`+strings.Repeat("a", 100)+`"}`, "application/json"
				}
				body := &countingBindingBody{reader: strings.NewReader(payload)}
				req := httptest.NewRequest(http.MethodPost, "/", nil)
				req.Body, req.ContentLength = body, length
				req.Header.Set("Content-Type", media)
				recorder := httptest.NewRecorder()
				engine.ServeHTTP(recorder, req)
				var sizeErr *http.MaxBytesError
				if !errors.Is(got, gola.ErrBodyTooLarge) || !errors.As(got, &sizeErr) || sizeErr.Limit != limit {
					t.Fatalf("size error = %v", got)
				}
				if body.read > limit+1 || (length > limit && body.read != 0) {
					t.Fatalf("read %d bytes for limit %d and length %d", body.read, limit, length)
				}
				if recorder.Code != http.StatusTeapot {
					t.Fatalf("binding committed response: %d", recorder.Code)
				}
			})
		}
	}
}

func TestJSONBodyLimitIncludesTrailingWhitespace(t *testing.T) {
	for _, count := range []int{30, 31} {
		engine := gola.New(gola.WithBodyLimit(32))
		var got error
		engine.POST("/", func(c *gola.Context) {
			var dst any
			got = c.ShouldBindJSON(&dst)
		})
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"+strings.Repeat(" ", count)))
		req.ContentLength = -1
		req.Header.Set("Content-Type", "application/json")
		engine.ServeHTTP(httptest.NewRecorder(), req)
		if (count == 30 && got != nil) || (count == 31 && !errors.Is(got, gola.ErrBodyTooLarge)) {
			t.Fatalf("trailing spaces %d: %v", count, got)
		}
	}
}

func TestBindingDefaultLimitWithoutMiddleware(t *testing.T) {
	engine := gola.New()
	var got error
	engine.POST("/", func(c *gola.Context) {
		var dst any
		got = c.ShouldBindJSON(&dst)
	})
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`"`+strings.Repeat("x", 1<<20)+`"`))
	req.ContentLength = -1
	req.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(httptest.NewRecorder(), req)
	var maxBytes *http.MaxBytesError
	if !errors.Is(got, gola.ErrBodyTooLarge) || !errors.As(got, &maxBytes) || maxBytes.Limit != 1<<20 {
		t.Fatalf("default binding limit error = %v", got)
	}
}

func TestFormMediaTypes(t *testing.T) {
	for _, media := range []string{"", "application/json", "multipart/form-data; boundary=test", "text/plain"} {
		engine := gola.New()
		engine.POST("/", func(c *gola.Context) {
			var dst struct {
				V string `form:"v"`
			}
			if err := c.ShouldBindForm(&dst); !errors.Is(err, gola.ErrUnsupportedMediaType) {
				t.Errorf("media %q: error = %v", media, err)
			}
		})
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("v=Danny"))
		req.Header.Set("Content-Type", media)
		engine.ServeHTTP(httptest.NewRecorder(), req)
	}
}

func TestBindingDoesNotChangeAbsentFieldsOrPartiallyConvert(t *testing.T) {
	type input struct {
		Name  string `form:"name"`
		Count int8   `form:"count"`
		Flag  *bool  `form:"flag"`
	}
	engine := gola.New()
	engine.GET("/", func(c *gola.Context) {
		flag := true
		dst := input{Name: "original", Count: 7, Flag: &flag}
		err := c.ShouldBindQuery(&dst)
		if c.Query("count") == "128" {
			if !errors.Is(err, gola.ErrBindingType) || dst.Name != "original" || dst.Count != 7 || dst.Flag != &flag || !flag {
				t.Errorf("partial mutation: dst=%+v error=%v", dst, err)
			}
		} else if err != nil || dst.Name != "Danny" || dst.Count != 7 || dst.Flag != &flag || !flag {
			t.Errorf("missing fields changed: dst=%+v error=%v", dst, err)
		}
	})
	for _, query := range []string{"name=Danny", "name=Danny&count=128&flag=false"} {
		engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/?"+query, nil))
	}
}

func TestBindingChunkedHTTPBody(t *testing.T) {
	engine := gola.New(gola.WithBodyLimit(16))
	requestErr := make(chan error, 1)
	engine.POST("/", func(c *gola.Context) {
		var dst any
		err := c.ShouldBindJSON(&dst)
		requestErr <- err
		if errors.Is(err, gola.ErrBodyTooLarge) {
			c.Status(http.StatusRequestEntityTooLarge)
			return
		}
		c.Status(http.StatusBadRequest)
	})
	server := httptest.NewServer(engine)
	defer server.Close()
	req, err := http.NewRequest(http.MethodPost, server.URL, io.NopCloser(strings.NewReader(`{"v":"`+strings.Repeat("a", 100)+`"}`)))
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = -1
	req.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge || !errors.Is(<-requestErr, gola.ErrBodyTooLarge) {
		t.Fatalf("chunked request status = %d", response.StatusCode)
	}
}

func TestPostFormRecordsErrorOnce(t *testing.T) {
	engine := gola.New(gola.WithBodyLimit(4))
	engine.POST("/", func(c *gola.Context) {
		if c.PostForm("v") != "" || c.PostForm("v") != "" {
			t.Error("unexpected values for oversized form")
		}
		if errs := c.Errors(); len(errs) != 1 || !errors.Is(errs[0], gola.ErrBodyTooLarge) {
			t.Errorf("PostForm errors = %v", errs)
		}
		var dst struct {
			V string `form:"v"`
		}
		if err := c.ShouldBindForm(&dst); !errors.Is(err, gola.ErrBodyTooLarge) {
			t.Errorf("cached form failure = %v", err)
		}
		c.Status(http.StatusAccepted)
	})
	req := httptest.NewRequest(http.MethodPost, "/?v=query", strings.NewReader("v=large"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("PostForm committed response: %d", recorder.Code)
	}
}
