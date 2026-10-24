package memory

import (
	"errors"
	"testing"
	"time"

	"github.com/islamghany/goplate/internal/cache"
	"github.com/islamghany/goplate/internal/ratelimit"
)

func TestCacheExpiry(t *testing.T) {
	now := time.Unix(0, 0)
	c := NewCache()
	c.now = func() time.Time { return now }
	ctx := t.Context()

	_ = c.Set(ctx, "k", []byte("v"), time.Minute)
	if b, err := c.Get(ctx, "k"); err != nil || string(b) != "v" {
		t.Fatalf("Get = %q, %v", b, err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := c.Get(ctx, "k"); !errors.Is(err, cache.ErrMiss) {
		t.Fatalf("expired key: %v", err)
	}
	_ = c.Set(ctx, "a", []byte("1"), 0)
	_ = c.Delete(ctx, "a")
	if _, err := c.Get(ctx, "a"); !errors.Is(err, cache.ErrMiss) {
		t.Fatalf("deleted key: %v", err)
	}
}

func TestLimiterBurstThenDeny(t *testing.T) {
	now := time.Unix(1000, 0)
	l := NewLimiter()
	l.now = func() time.Time { return now }
	p := ratelimit.Policy{Name: "t", Rate: 60, Period: time.Minute, Burst: 3}
	ctx := t.Context()

	for i := range 3 {
		if d, _ := l.Allow(ctx, "k", p); !d.Allowed {
			t.Fatalf("request %d within burst denied", i)
		}
	}
	d, _ := l.Allow(ctx, "k", p)
	if d.Allowed || d.RetryAfter <= 0 || d.RetryAfter > time.Second {
		t.Fatalf("4th request: %+v", d)
	}
	if d, _ := l.Allow(ctx, "other", p); !d.Allowed {
		t.Fatal("keys must have independent buckets")
	}
	now = now.Add(time.Second) // 1 token per second
	if d, _ := l.Allow(ctx, "k", p); !d.Allowed {
		t.Fatal("bucket must refill")
	}
}
