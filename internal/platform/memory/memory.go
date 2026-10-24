// Package memory provides in-process implementations of cache.Store and
// ratelimit.Limiter. They are meant for tests, local development and
// single-instance deployments: state is per process and lost on restart.
package memory

import (
	"context"
	"math"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/islamghany/goplate/internal/cache"
	"github.com/islamghany/goplate/internal/ratelimit"
)

// sweepEvery bounds memory: every N writes, expired entries are dropped.
const sweepEvery = 1024

// Cache is a map with per-key expiry. Safe for concurrent use.
type Cache struct {
	mu     sync.Mutex
	items  map[string]item
	writes int
	now    func() time.Time
}

type item struct {
	val     []byte
	expires time.Time // zero means never
}

// NewCache returns an empty Cache.
func NewCache() *Cache {
	return &Cache{items: map[string]item{}, now: time.Now}
}

var _ cache.Store = (*Cache)(nil)

func (c *Cache) Get(_ context.Context, key string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	it, ok := c.items[key]
	if !ok || c.expired(it) {
		delete(c.items, key)
		return nil, cache.ErrMiss
	}
	return append([]byte(nil), it.val...), nil
}

func (c *Cache) Set(_ context.Context, key string, val []byte, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	it := item{val: append([]byte(nil), val...)}
	if ttl > 0 {
		it.expires = c.now().Add(ttl)
	}
	c.items[key] = it
	if c.writes++; c.writes%sweepEvery == 0 {
		for k, v := range c.items {
			if c.expired(v) {
				delete(c.items, k)
			}
		}
	}
	return nil
}

func (c *Cache) Delete(_ context.Context, keys ...string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, k := range keys {
		delete(c.items, k)
	}
	return nil
}

func (c *Cache) expired(it item) bool {
	return !it.expires.IsZero() && c.now().After(it.expires)
}

// Limiter is a token-bucket limiter per key.
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	calls   int
	now     func() time.Time
}

type bucket struct {
	lim      *rate.Limiter
	lastSeen time.Time
}

// NewLimiter returns an empty Limiter.
func NewLimiter() *Limiter {
	return &Limiter{buckets: map[string]*bucket{}, now: time.Now}
}

var _ ratelimit.Limiter = (*Limiter)(nil)

func (l *Limiter) Allow(_ context.Context, key string, p ratelimit.Policy) (ratelimit.Decision, error) {
	now := l.now()
	perSecond := rate.Limit(float64(p.Rate) / p.Period.Seconds())
	burst := max(p.Burst, 1)

	l.mu.Lock()
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{lim: rate.NewLimiter(perSecond, burst)}
		l.buckets[key] = b
	}
	b.lastSeen = now
	if l.calls++; l.calls%sweepEvery == 0 {
		for k, v := range l.buckets {
			if now.Sub(v.lastSeen) > p.Period*2 {
				delete(l.buckets, k)
			}
		}
	}
	l.mu.Unlock()

	r := b.lim.ReserveN(now, 1)
	if delay := r.DelayFrom(now); delay > 0 {
		r.CancelAt(now)
		return ratelimit.Decision{Allowed: false, Limit: p.Rate, RetryAfter: delay, ResetAfter: delay}, nil
	}
	tokens := b.lim.TokensAt(now)
	resetAfter := time.Duration((float64(burst) - tokens) / float64(perSecond) * float64(time.Second))
	return ratelimit.Decision{
		Allowed:    true,
		Limit:      p.Rate,
		Remaining:  int(math.Max(0, math.Floor(tokens))),
		ResetAfter: max(resetAfter, 0),
	}, nil
}
