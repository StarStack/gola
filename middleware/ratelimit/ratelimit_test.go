package ratelimit

import (
	"math"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/starstack/gola"
)

func engine(t *testing.T, config Config, now func() time.Time) *gola.Engine {
	t.Helper()
	mw, err := newLimiter(config, now)
	if err != nil {
		t.Fatal(err)
	}
	e := gola.New()
	e.Use(mw)
	e.GET("/", func(c *gola.Context) { _ = c.String(200, "allowed") })
	return e
}

func request(e *gola.Engine, peer, forwarded string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = peer
	if forwarded != "" {
		r.Header.Set("X-Forwarded-For", forwarded)
	}
	w := httptest.NewRecorder()
	e.ServeHTTP(w, r)
	return w
}

func TestBurstRefillAndRetryAfter(t *testing.T) {
	now := time.Unix(0, 0)
	e := engine(t, Config{Rate: 0.4, Burst: 2}, func() time.Time { return now })
	for i := 0; i < 2; i++ {
		if w := request(e, "192.0.2.1:1234", ""); w.Code != 200 {
			t.Fatalf("burst request rejected: %d", w.Code)
		}
	}
	w := request(e, "192.0.2.1:1234", "")
	if w.Code != 429 || w.Header().Get("Retry-After") != "3" || w.Header().Get("Cache-Control") != "no-store" || w.Body.Len() != 0 {
		t.Fatalf("rejection=%d %v %q", w.Code, w.Header(), w.Body.String())
	}
	now = now.Add(2500 * time.Millisecond)
	if w := request(e, "192.0.2.1:9999", ""); w.Code != 200 {
		t.Fatal("quota not refilled")
	}
	if w := request(e, "192.0.2.1:1234", ""); w.Code != 429 {
		t.Fatal("too many tokens refilled")
	}
}

func TestProxyTrustAndFallback(t *testing.T) {
	now := time.Now()
	e := engine(t, Config{Rate: 1, Burst: 1}, func() time.Time { return now })
	if request(e, "192.0.2.1:1234", "198.51.100.1").Code != 200 || request(e, "192.0.2.1:1234", "198.51.100.2").Code != 429 {
		t.Fatal("untrusted forwarding bypassed quota")
	}
	if request(e, "unix-peer-one", "").Code != 200 || request(e, "unix-peer-two", "").Code != 429 {
		t.Fatal("unknown peers did not share fallback")
	}
	trusted := engine(t, Config{Rate: 1, Burst: 1}, func() time.Time { return now })
	if err := trusted.SetTrustedProxies([]string{"192.0.2.1"}); err != nil {
		t.Fatal(err)
	}
	if request(trusted, "192.0.2.1:1234", "198.51.100.1").Code != 200 || request(trusted, "192.0.2.1:1234", "198.51.100.2").Code != 200 {
		t.Fatal("trusted proxy identities were not separated")
	}
}

func TestCapacityAndExpiration(t *testing.T) {
	now := time.Now()
	e := engine(t, Config{Rate: 1, Burst: 1, MaxKeys: 2, IdleTimeout: 2 * time.Second}, func() time.Time { return now })
	if request(e, "192.0.2.1", "").Code != 200 || request(e, "192.0.2.2", "").Code != 200 {
		t.Fatal("initial identities rejected")
	}
	for i := 3; i < 200; i++ {
		w := request(e, "192.0.2."+strconv.Itoa(i), "")
		if w.Code != 429 || w.Header().Get("Retry-After") != "2" {
			t.Fatal("full capacity admitted new identity")
		}
	}
	if request(e, "192.0.2.1", "").Code != 429 {
		t.Fatal("new identities evicted an active quota")
	}
	now = now.Add(2 * time.Second)
	if request(e, "192.0.2.3", "").Code != 200 {
		t.Fatal("idle capacity was not released")
	}
}

func TestCustomKeyBoundsAndConcurrency(t *testing.T) {
	now := time.Now()
	e := engine(t, Config{Rate: 1, Burst: 10, Key: func(c *gola.Context) string { return strings.Repeat(c.Request.RemoteAddr, 300) }}, func() time.Time { return now })
	var accepted atomic.Int64
	var wg sync.WaitGroup
	for i := range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if request(e, strconv.Itoa(i), "").Code == 200 {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 10 {
		t.Fatalf("accepted %d, want 10", accepted.Load())
	}
}

func TestInvalidConfig(t *testing.T) {
	for _, c := range []Config{
		{}, {Rate: math.NaN(), Burst: 1}, {Rate: math.Inf(1), Burst: 1},
		{Rate: -1, Burst: 1}, {Rate: 1, Burst: -1}, {Rate: 1, Burst: 1_000_001},
		{Rate: 1, Burst: 1, MaxKeys: -1}, {Rate: 1, Burst: 1, MaxKeys: 1_000_001},
		{Rate: 1, Burst: 1, IdleTimeout: -time.Second},
		{Rate: 1, Burst: 1, IdleTimeout: 25 * time.Hour},
		{Rate: 0.1, Burst: 2, IdleTimeout: time.Second},
	} {
		if _, err := New(c); err == nil {
			t.Fatalf("accepted invalid configuration: %+v", c)
		}
	}
}
