package gola

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestClientIPTrustBoundary(t *testing.T) {
	tests := []struct {
		name, remote string
		trusted      []string
		xff          []string
		real         string
		allowReal    bool
		want         string
	}{
		{"default ignores spoof", "192.0.2.1:80", nil, []string{"198.51.100.1"}, "198.51.100.2", true, "192.0.2.1"},
		{"rightmost untrusted regression", "1.1.1.1:1234", []string{"1.1.1.1"}, []string{"7.7.7.7, 6.6.6.6"}, "", false, "6.6.6.6"},
		{"multiple proxies", "10.0.0.1:80", []string{"10.0.0.0/8"}, []string{"198.51.100.1, 10.0.0.2"}, "", false, "198.51.100.1"},
		{"repeated field joins", "10.0.0.1:80", []string{"10.0.0.0/8"}, []string{"198.51.100.1", "10.0.0.2"}, "", false, "198.51.100.1"},
		{"all trusted", "10.0.0.1:80", []string{"10.0.0.0/8"}, []string{"10.0.0.3, 10.0.0.2"}, "", false, "10.0.0.3"},
		{"invalid no fallback", "10.0.0.1:80", []string{"10.0.0.0/8"}, []string{"garbage"}, "198.51.100.1", true, "10.0.0.1"},
		{"invalid earlier hop", "10.0.0.1:80", []string{"10.0.0.0/8"}, []string{"garbage, 198.51.100.1"}, "", false, "10.0.0.1"},
		{"empty xff no fallback", "10.0.0.1:80", []string{"10.0.0.0/8"}, []string{""}, "198.51.100.1", true, "10.0.0.1"},
		{"empty element", "10.0.0.1:80", []string{"10.0.0.0/8"}, []string{"198.51.100.1,"}, "", false, "10.0.0.1"},
		{"port in forwarded IP rejected", "10.0.0.1:80", []string{"10.0.0.0/8"}, []string{"198.51.100.1:1234"}, "", false, "10.0.0.1"},
		{"real explicitly enabled", "10.0.0.1:80", []string{"10.0.0.0/8"}, nil, "198.51.100.1", true, "198.51.100.1"},
		{"real default disabled", "10.0.0.1:80", []string{"10.0.0.0/8"}, nil, "198.51.100.1", false, "10.0.0.1"},
		{"xff precedence", "10.0.0.1:80", []string{"10.0.0.0/8"}, []string{"198.51.100.1"}, "198.51.100.2", true, "198.51.100.1"},
		{"IPv6 peer", "[2001:db8::1]:80", nil, nil, "", false, "2001:db8::1"},
		{"IPv6 chain", "[2001:db8::1]:80", []string{"2001:db8::/32"}, []string{"2001:db9::1, 2001:db8::2"}, "", false, "2001:db9::1"},
		{"IPv4 mapped IPv6", "[::ffff:10.0.0.1]:80", []string{"::ffff:10.0.0.0/104"}, []string{"::ffff:198.51.100.1"}, "", false, "198.51.100.1"},
		{"IPv6 zone peer", "[fe80::1%en0]:80", nil, nil, "", false, "fe80::1"},
		{"forwarded zone rejected", "10.0.0.1:80", []string{"10.0.0.1"}, []string{"fe80::1%en0"}, "", false, "10.0.0.1"},
		{"bare IPv4", "192.0.2.1", nil, nil, "", false, "192.0.2.1"},
		{"invalid peer", "attacker.example:1234", []string{"0.0.0.0/0"}, []string{"198.51.100.1"}, "", false, ""},
		{"too many hops", "10.0.0.1:80", []string{"10.0.0.1"}, []string{strings.Repeat("10.0.0.1,", 128) + "198.51.100.1"}, "", false, "10.0.0.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := New(WithTrustedRealIP(tt.allowReal))
			if err := e.SetTrustedProxies(tt.trusted); err != nil {
				t.Fatal(err)
			}
			e.GET("/", func(c *Context) { _ = c.String(http.StatusOK, "%s", c.ClientIP()) })
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tt.remote
			if tt.xff != nil {
				r.Header["X-Forwarded-For"] = tt.xff
			}
			r.Header.Set("X-Real-IP", tt.real)
			w := httptest.NewRecorder()
			e.ServeHTTP(w, r)
			if got := w.Body.String(); got != tt.want {
				t.Fatalf("ClientIP=%q want %q", got, tt.want)
			}
		})
	}
}

func TestTrustedProxiesConfiguration(t *testing.T) {
	e := New()
	if err := e.SetTrustedProxies([]string{"10.0.0.1"}); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"", "hostname", "10.0.0.1:80", "10.0.0.0/33", "fe80::1%en0", "::ffff:0.0.0.0/64"} {
		if err := e.SetTrustedProxies([]string{"192.0.2.1", invalid}); err == nil {
			t.Fatalf("accepted %q", invalid)
		}
	}
	e.GET("/", func(c *Context) { _ = c.String(200, "%s", c.ClientIP()) })
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.1:80"
	r.Header.Set("X-Forwarded-For", "198.51.100.1")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, r)
	if w.Body.String() != "198.51.100.1" {
		t.Fatal("invalid updates changed existing proxy policy")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("mutation after serving did not panic")
		}
	}()
	_ = e.SetTrustedProxies(nil)
}

func FuzzTrustedProxyParsing(f *testing.F) {
	for _, seed := range []string{"1.1.1.1, 10.0.0.1", "bad, 1.1.1.1", "2001:db8::1", "", "::ffff:127.0.0.1", "127.0.0.1:80", "1.1.1.1\r\nforged"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		e := New()
		if err := e.SetTrustedProxies([]string{"10.0.0.1"}); err != nil {
			t.Fatal(err)
		}
		e.GET("/", func(c *Context) {
			ip := c.ClientIP()
			if _, err := netip.ParseAddr(ip); err != nil {
				t.Fatalf("returned non-IP %q", ip)
			}
		})
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = "10.0.0.1:80"
		r.Header.Set("X-Forwarded-For", value)
		e.ServeHTTP(httptest.NewRecorder(), r)
	})
}
