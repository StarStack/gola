package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBindingResponses(t *testing.T) {
	r := newRouter()
	for _, tc := range []struct {
		name, contentType, body string
		status                  int
	}{
		{"valid", "application/json", `{"name":"Danny"}`, 201},
		{"validation", "application/json", `{"name":""}`, 400},
		{"syntax", "application/json", `{`, 400},
		{"strict", "application/json", `{"name":"Danny","admin":true}`, 400},
		{"multiple", "application/json", `{"name":"Danny"}{}`, 400},
		{"media", "text/plain", `{"name":"Danny"}`, 415},
		{"oversize", "application/json", strings.Repeat(" ", 1<<20+1), 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", tc.contentType)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.status, w.Body.String())
			}
		})
	}
}
