// Package ratelimit provides a bounded, process-local token bucket limiter.
package ratelimit

import (
	"container/list"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/starstack/gola"
)

// Config is copied by New. Rate is tokens per second and Burst is the maximum
// immediately available tokens (at most 1,000,000); each request costs one token.
// Both are required.
type Config struct {
	Rate  float64
	Burst int
	// MaxKeys defaults to 10,000 and must not exceed 1,000,000. New identities
	// receive 429 when capacity is exhausted; active buckets are never evicted.
	MaxKeys int
	// IdleTimeout defaults to ten minutes and is capped at 24 hours. It must
	// be at least Burst/Rate so expiration cannot refill a bucket prematurely.
	IdleTimeout time.Duration
	// Key defaults to Context.ClientIP, honoring the engine's trusted proxies.
	// Empty keys and keys longer than 256 bytes share one fallback bucket.
	// Custom functions must be concurrency-safe and use authenticated identity.
	Key func(*gola.Context) string
}

type bucket struct {
	key      string
	tokens   float64
	updated  time.Time
	lastSeen time.Time
}

type limiter struct {
	mu     sync.Mutex
	config Config
	keys   map[string]*list.Element
	order  list.List
	now    func() time.Time
}

// New validates all bounds before constructing middleware. State is local to
// this middleware instance; separate processes do not share a quota.
func New(config Config) (gola.HandlerFunc, error) {
	return newLimiter(config, time.Now)
}

func newLimiter(config Config, now func() time.Time) (gola.HandlerFunc, error) {
	if config.MaxKeys == 0 {
		config.MaxKeys = 10_000
	}
	if config.IdleTimeout == 0 {
		config.IdleTimeout = 10 * time.Minute
	}
	if config.Rate <= 0 || math.IsNaN(config.Rate) || math.IsInf(config.Rate, 0) || config.Burst <= 0 || config.Burst > 1_000_000 || config.MaxKeys < 1 || config.MaxKeys > 1_000_000 || config.IdleTimeout <= 0 || config.IdleTimeout > 24*time.Hour || float64(config.Burst)/config.Rate > config.IdleTimeout.Seconds() {
		return nil, fmt.Errorf("ratelimit: invalid rate, burst, capacity or idle timeout")
	}
	if config.Key == nil {
		config.Key = (*gola.Context).ClientIP
	}
	l := &limiter{config: config, keys: make(map[string]*list.Element), now: now}
	return func(c *gola.Context) {
		key := config.Key(c)
		// Prefix valid identities to prevent collision with the shared fallback.
		if key == "" || len(key) > 256 {
			key = "fallback"
		} else {
			key = "identity:" + key
		}
		if ok, retry := l.allow(key); !ok {
			c.Header("Retry-After", strconv.FormatInt(int64(math.Ceil(retry.Seconds())), 10))
			c.Header("Cache-Control", "no-store")
			c.AbortWithStatus(http.StatusTooManyRequests)
			return
		}
	}, nil
}

func (l *limiter) allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for front := l.order.Front(); front != nil; front = l.order.Front() {
		b := front.Value.(*bucket)
		if now.Sub(b.lastSeen) < l.config.IdleTimeout {
			break
		}
		delete(l.keys, b.key)
		l.order.Remove(front)
	}
	elem := l.keys[key]
	if elem == nil {
		if len(l.keys) == l.config.MaxKeys {
			return false, max(time.Second, l.config.IdleTimeout-now.Sub(l.order.Front().Value.(*bucket).lastSeen))
		}
		b := &bucket{key: key, tokens: float64(l.config.Burst), updated: now, lastSeen: now}
		elem = l.order.PushBack(b)
		l.keys[key] = elem
	}
	b := elem.Value.(*bucket)
	if elapsed := now.Sub(b.updated); elapsed > 0 {
		b.tokens = min(float64(l.config.Burst), b.tokens+elapsed.Seconds()*l.config.Rate)
		b.updated = now
	}
	b.lastSeen = now
	l.order.MoveToBack(elem)
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, max(time.Nanosecond, time.Duration(math.Ceil((1-b.tokens)/l.config.Rate*float64(time.Second))))
}
