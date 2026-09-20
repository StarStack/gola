package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRenderRoutes(t *testing.T) {
	r := newRouter()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/hello?name=%3Cscript%3E", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Hello &lt;script&gt;") {
		t.Fatal(w)
	}
	for _, body := range []string{`<greeting><name>Danny</name></greeting>`, `<!DOCTYPE greeting><greeting/>`} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/xml", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/xml")
		r.ServeHTTP(w, req)
		if strings.HasPrefix(body, "<!") {
			if w.Code != 400 {
				t.Fatal(w)
			}
		} else if w.Code != 200 || w.Body.String() != body {
			t.Fatal(w)
		}
	}
}
