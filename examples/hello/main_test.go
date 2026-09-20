package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHello(t *testing.T) {
	w := httptest.NewRecorder()
	newRouter().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/hello/Danny", nil))
	if w.Code != http.StatusOK || w.Body.String() != `{"hello":"Danny"}` {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}
