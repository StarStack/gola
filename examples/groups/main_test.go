package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDemoAuthentication(t *testing.T) {
	r := newRouter()
	for _, token := range []string{"", "Bearer invalid", "Bearer demo-only-token"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
		req.Header.Set("Authorization", token)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if token == "Bearer demo-only-token" {
			if w.Code != 200 || !strings.Contains(w.Body.String(), "demo-danny") {
				t.Fatalf("allowed = %d %s", w.Code, w.Body.String())
			}
		} else if w.Code != 401 || strings.Contains(w.Body.String(), "demo-danny") {
			t.Fatalf("denied = %d %s", w.Code, w.Body.String())
		}
	}
}
