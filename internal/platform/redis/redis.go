// Package redis adapts Redis (or any protocol-compatible server such as
// Valkey) to goplate's ports: cache.Store (ADR-0009) and ratelimit.Limiter
// (ADR-0013). Nothing outside this package imports go-redis.
package redis

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-redis/redis_rate/v10"
	"github.com/redis/go-redis/extra/redisotel/v9"
	goredis "github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/islamghany/goplate/internal/cache"
	"github.com/islamghany/goplate/internal/ratelimit"
)

// Open connects to url (redis://user:pass@host:6379/0), instruments the
// client with OpenTelemetry and verifies connectivity.
func Open(ctx context.Context, url string) (*goredis.Client, error) {
	opt, err := goredis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	rdb := goredis.NewClient(opt)
	if err := redisotel.InstrumentTracing(rdb); err != nil {
		return nil, fmt.Errorf("instrument redis tracing: %w", err)
	}
	if err := redisotel.InstrumentMetrics(rdb); err != nil {
		return nil, fmt.Errorf("instrument redis metrics: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return rdb, nil
}

// Cache implements cache.Store. Keys are namespaced with prefix.
type Cache struct {
	rdb     *goredis.Client
	log     *slog.Logger
	prefix  string
	lookups metric.Int64Counter
}

// NewCache returns a Cache. prefix is typically "<app>:".
func NewCache(rdb *goredis.Client, log *slog.Logger, prefix string) *Cache {
	lookups, _ := otel.Meter("goplate/cache").Int64Counter("cache.lookups",
		metric.WithDescription("Cache lookups by result (hit, miss, error)."))
	return &Cache{rdb: rdb, log: log, prefix: prefix, lookups: lookups}
}

var _ cache.Store = (*Cache)(nil)

func (c *Cache) Get(ctx context.Context, key string) ([]byte, error) {
	b, err := c.rdb.Get(ctx, c.prefix+key).Bytes()
	switch {
	case errors.Is(err, goredis.Nil):
		c.count(ctx, "miss")
		return nil, cache.ErrMiss
	case err != nil:
		c.count(ctx, "error")
		c.log.WarnContext(ctx, "cache get failed", "key", key, "err", err)
		return nil, fmt.Errorf("redis get: %w", err)
	}
	c.count(ctx, "hit")
	return b, nil
}

func (c *Cache) Set(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	if err := c.rdb.Set(ctx, c.prefix+key, val, ttl).Err(); err != nil {
		c.log.WarnContext(ctx, "cache set failed", "key", key, "err", err)
		return fmt.Errorf("redis set: %w", err)
	}
	return nil
}

func (c *Cache) Delete(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	full := make([]string, len(keys))
	for i, k := range keys {
		full[i] = c.prefix + k
	}
	if err := c.rdb.Del(ctx, full...).Err(); err != nil {
		c.log.WarnContext(ctx, "cache delete failed", "keys", keys, "err", err)
		return fmt.Errorf("redis del: %w", err)
	}
	return nil
}

func (c *Cache) count(ctx context.Context, result string) {
	c.lookups.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
}

// Limiter implements ratelimit.Limiter with the GCRA algorithm, executed
// atomically in Redis, so limits hold across any number of API instances.
type Limiter struct {
	l      *redis_rate.Limiter
	prefix string
}

// NewLimiter returns a Limiter.
func NewLimiter(rdb *goredis.Client, prefix string) *Limiter {
	return &Limiter{l: redis_rate.NewLimiter(rdb), prefix: prefix}
}

var _ ratelimit.Limiter = (*Limiter)(nil)

func (l *Limiter) Allow(ctx context.Context, key string, p ratelimit.Policy) (ratelimit.Decision, error) {
	res, err := l.l.Allow(ctx, l.prefix+"rl:"+key, redis_rate.Limit{Rate: p.Rate, Burst: p.Burst, Period: p.Period})
	if err != nil {
		return ratelimit.Decision{}, fmt.Errorf("redis rate limit: %w", err)
	}
	return ratelimit.Decision{
		Allowed:    res.Allowed > 0,
		Limit:      p.Rate,
		Remaining:  res.Remaining,
		RetryAfter: max(res.RetryAfter, 0),
		ResetAfter: max(res.ResetAfter, 0),
	}, nil
}
