package gola_test

import (
	"bytes"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	gola "github.com/starstack/gola"
)

func FuzzMultipartParsing(f *testing.F) {
	valid, media := makeMultipartPayload(f, [][2]string{{"name", "Danny"}}, []multipartTestFile{{"upload", "sample.txt", "sample"}})
	f.Add(valid, media)
	f.Add(valid[:len(valid)-12], media)
	f.Add(append(append([]byte(nil), valid...), bytes.Repeat([]byte("!"), 2048)...), media)
	f.Add([]byte(""), "multipart/form-data; boundary=x")
	f.Add([]byte("--x--\r\n"), "multipart/form-data; boundary=x")
	f.Add([]byte("name=value"), "application/x-www-form-urlencoded")
	f.Fuzz(func(t *testing.T, raw []byte, contentType string) {
		if len(raw) > 8192 || len(contentType) > 256 || strings.ContainsAny(contentType, "\r\n") {
			t.Skip()
		}
		const limit = 2048
		body := &countingBindingBody{reader: bytes.NewReader(raw)}
		engine := gola.New(gola.WithBodyLimit(limit), gola.WithMultipartMemory(32))
		engine.POST("/", func(c *gola.Context) {
			form, err := c.MultipartForm()
			read := body.read
			cached, cachedErr := c.MultipartForm()
			if cached != form || cachedErr != err || body.read != read {
				t.Fatal("multipart result was not cached")
			}
			if err == nil {
				if form == nil || len(raw) > limit {
					t.Fatal("accepted nil form or oversized body")
				}
			} else {
				var bindingErr *gola.BindingError
				if form != nil || !errors.As(err, &bindingErr) {
					t.Fatalf("failure exposed partial form or unclassified error: %v", err)
				}
			}
			_, _ = c.FormFile("upload")
			if body.read > limit+1 || c.Writer.Written() || c.IsAborted() {
				t.Fatal("multipart exceeded its read allowance or changed the response")
			}
		})
		req := multipartRequest(nil, contentType)
		req.Body, req.ContentLength = body, -1
		engine.ServeHTTP(httptest.NewRecorder(), req)
	})
}
