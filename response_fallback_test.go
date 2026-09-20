package gola_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	gola "github.com/starstack/gola"
)

func TestEmptyStandardFallbackStatusMatchesAccessLog(t *testing.T) {
	for _, fallback := range []string{"NoRoute", "NoMethod"} {
		t.Run(fallback, func(t *testing.T) {
			var logs bytes.Buffer
			engine := gola.Default(gola.WithLogger(slog.New(slog.NewJSONHandler(&logs, nil))))
			empty := gola.WrapF(func(http.ResponseWriter, *http.Request) {})
			engine.GET("/registered", func(c *gola.Context) { c.Status(http.StatusNoContent) })
			method, path := http.MethodGet, "/missing"
			if fallback == "NoRoute" {
				engine.NoRoute(empty)
			} else {
				engine.NoMethod(empty)
				method, path = http.MethodPost, "/registered"
			}
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, httptest.NewRequest(method, path, nil))
			if recorder.Result().StatusCode != http.StatusOK || recorder.Body.Len() != 0 {
				t.Fatalf("empty standard fallback = %d %q", recorder.Result().StatusCode, recorder.Body.String())
			}
			var entry struct {
				Status int    `json:"status"`
				Route  string `json:"route"`
			}
			if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
				t.Fatalf("access log: %v: %s", err, logs.String())
			}
			if entry.Status != http.StatusOK || entry.Route != "" {
				t.Fatalf("fallback access log disagrees with response: %+v", entry)
			}
		})
	}
}
