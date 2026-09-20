package cors

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/starstack/gola"
)

func TestConfigValidation(t *testing.T) {
	for _, config := range []Config{
		{}, {AllowOrigins: []string{"https://example.com"}},
		{AllowOrigins: []string{"*"}, AllowMethods: []string{"GET"}, AllowCredentials: true},
		{AllowOrigins: []string{"*", "https://example.com"}, AllowMethods: []string{"GET"}},
		{AllowOrigins: []string{"https://example.com/"}, AllowMethods: []string{"GET"}},
		{AllowOrigins: []string{"https://example.com#"}, AllowMethods: []string{"GET"}},
		{AllowOrigins: []string{"https://user@example.com"}, AllowMethods: []string{"GET"}},
		{AllowOrigins: []string{"null"}, AllowMethods: []string{"GET"}},
		{AllowOrigins: []string{"https://*.example.com"}, AllowMethods: []string{"GET"}},
		{AllowOrigins: []string{"https://example.com"}, AllowMethods: []string{"*"}},
		{AllowOrigins: []string{"https://example.com"}, AllowMethods: []string{"GET\r\nX-Forged: yes"}},
		{AllowOrigins: []string{"https://example.com"}, AllowMethods: []string{"GET"}, AllowHeaders: []string{"*"}},
		{AllowOrigins: []string{"https://example.com"}, AllowMethods: []string{"GET"}, ExposeHeaders: []string{"bad header"}},
		{AllowOrigins: []string{"https://example.com"}, AllowMethods: []string{"GET"}, MaxAge: -time.Second},
	} {
		if _, err := New(config); err == nil {
			t.Errorf("accepted invalid config %+v", config)
		}
	}
}

func TestCORSPreflightAndActualRequests(t *testing.T) {
	middleware, err := New(Config{
		AllowOrigins: []string{"https://app.example.com"}, AllowMethods: []string{"GET", "POST"},
		AllowHeaders: []string{"Authorization", "Content-Type"}, ExposeHeaders: []string{"X-Request-ID"},
		AllowCredentials: true, MaxAge: 10 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, method, origin, requestedMethod, requestedHeaders string
		status                                                  int
		allow, auth                                             bool
	}{
		{"valid preflight", "OPTIONS", "https://app.example.com", "POST", "authorization, content-type", 204, true, false},
		{"denied origin", "OPTIONS", "https://evil.example.com", "POST", "Authorization", 403, false, false},
		{"suffix attack", "OPTIONS", "https://app.example.com.evil.test", "POST", "Authorization", 403, false, false},
		{"null origin", "OPTIONS", "null", "POST", "Authorization", 403, false, false},
		{"denied method", "OPTIONS", "https://app.example.com", "DELETE", "Authorization", 403, false, false},
		{"denied header", "OPTIONS", "https://app.example.com", "POST", "X-Special", 403, false, false},
		{"bad header token", "OPTIONS", "https://app.example.com", "POST", "Authorization,", 403, false, false},
		{"actual still authenticates", "GET", "https://app.example.com", "", "", 401, true, true},
		{"actual denied origin", "GET", "https://evil.test", "", "", 403, false, false},
		{"actual denied method", "DELETE", "https://app.example.com", "", "", 403, false, false},
		{"non CORS", "GET", "", "", "", 401, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := gola.New()
			called := false
			e.Use(func(c *gola.Context) { c.Header("Vary", "Accept-Encoding") }, middleware, func(c *gola.Context) { called = true; c.AbortWithStatus(401) })
			r := httptest.NewRequest(tt.method, "/not-registered", nil)
			if tt.origin != "" {
				r.Header.Set("Origin", tt.origin)
			}
			if tt.requestedMethod != "" {
				r.Header.Set("Access-Control-Request-Method", tt.requestedMethod)
			}
			if tt.requestedHeaders != "" {
				r.Header.Set("Access-Control-Request-Headers", tt.requestedHeaders)
			}
			w := httptest.NewRecorder()
			e.ServeHTTP(w, r)
			if w.Code != tt.status || called != tt.auth {
				t.Fatalf("status=%d auth=%v", w.Code, called)
			}
			if got := w.Header().Get("Access-Control-Allow-Origin"); (got != "") != tt.allow || (tt.allow && got != tt.origin) {
				t.Fatalf("unexpected allow origin %q", got)
			}
			if (w.Header().Get("Access-Control-Allow-Credentials") == "true") != tt.allow {
				t.Fatal("incorrect credentials response")
			}
			vary := strings.Join(w.Header().Values("Vary"), ", ")
			if !strings.Contains(vary, "Origin") || !strings.Contains(vary, "Accept-Encoding") {
				t.Fatalf("missing Vary: %q", vary)
			}
			if tt.requestedMethod != "" && (!strings.Contains(vary, "Access-Control-Request-Method") || !strings.Contains(vary, "Access-Control-Request-Headers")) {
				t.Fatalf("missing preflight Vary: %q", vary)
			}
			if tt.status == 204 && (w.Body.Len() != 0 || w.Header().Get("Access-Control-Max-Age") != "600") {
				t.Fatal("incorrect preflight response")
			}
		})
	}
}

func TestCORSPolicyCopiedAndWildcard(t *testing.T) {
	config := Config{AllowOrigins: []string{"https://app.example.com"}, AllowMethods: []string{"GET"}}
	mw, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	config.AllowOrigins[0] = "https://evil.test"
	config.AllowMethods[0] = "DELETE"
	e := gola.New()
	e.Use(mw)
	e.GET("/", func(c *gola.Context) { c.Status(204) })
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Origin", "https://app.example.com")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatal("configuration was not copied")
	}
	mw, err = New(Config{AllowOrigins: []string{"*"}, AllowMethods: []string{"GET"}})
	if err != nil {
		t.Fatal(err)
	}
	e = gola.New()
	e.Use(mw)
	e.GET("/", func(c *gola.Context) { c.Status(204) })
	w = httptest.NewRecorder()
	e.ServeHTTP(w, r)
	if w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != "*" || w.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatal("bad wildcard response")
	}
}

func TestCORSRejectsDuplicateAndEmptyHeaders(t *testing.T) {
	for _, headers := range []http.Header{
		{"Origin": {"https://app.example.com", "https://evil.test"}},
		{"Origin": {"https://app.example.com"}, "Access-Control-Request-Method": {"POST", "GET"}},
		{"Origin": {"https://app.example.com"}, "Access-Control-Request-Method": {""}},
	} {
		mw, err := New(Config{AllowOrigins: []string{"https://app.example.com"}, AllowMethods: []string{"GET", "POST"}})
		if err != nil {
			t.Fatal(err)
		}
		e := gola.New()
		e.Use(mw)
		r := httptest.NewRequest("OPTIONS", "/", nil)
		r.Header = headers
		w := httptest.NewRecorder()
		e.ServeHTTP(w, r)
		if w.Code != 403 || w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("ambiguous headers allowed")
		}
	}
}

func FuzzCORSDeniedOrigin(f *testing.F) {
	for _, origin := range []string{"https://evil.test", "null", "https://app.example.com.evil", "https://app.example.com", "\r\n"} {
		f.Add(origin)
	}
	f.Fuzz(func(t *testing.T, origin string) {
		mw, err := New(Config{AllowOrigins: []string{"https://app.example.com"}, AllowMethods: []string{"GET"}, AllowCredentials: true})
		if err != nil {
			t.Fatal(err)
		}
		e := gola.New()
		e.Use(mw)
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		e.ServeHTTP(w, r)
		if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" && (origin != "https://app.example.com" || got != origin) {
			t.Fatalf("reflected denied origin %q", got)
		}
	})
}
