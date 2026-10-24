// Package cache holds the shared vocabulary for caching (ADR-0009).
//
// Modules declare their own small cache interface (usually identical to
// Store) and use GetOrLoad for the cache-aside pattern. Adapters live in
// internal/platform/redis and internal/platform/memory and return ErrMiss on
// a miss.
//
// The cache is never the source of truth: GetOrLoad treats every cache error
// as a miss and ignores write failures, so a cache outage degrades latency,
// not availability. Adapters are responsible for logging and counting their
// own errors.
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"golang.org/x/sync/singleflight"
)

// ErrMiss is returned by Get when the key does not exist.
var ErrMiss = errors.New("cache: miss")

// Store is the minimal key/value contract adapters implement.
type Store interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, val []byte, ttl time.Duration) error
	Delete(ctx context.Context, keys ...string) error
}

// Loader wraps a Store with JSON encoding and request coalescing. Create one
// per module (or per value type) and reuse it.
type Loader[T any] struct {
	store Store
	ttl   time.Duration
	group singleflight.Group
}

// NewLoader returns a Loader that caches values for ttl.
func NewLoader[T any](store Store, ttl time.Duration) *Loader[T] {
	return &Loader[T]{store: store, ttl: ttl}
}

// GetOrLoad returns the cached value for key, or calls load, caches its
// result and returns it. Concurrent misses for the same key share one load
// (singleflight), which protects the database from thundering herds.
func (l *Loader[T]) GetOrLoad(ctx context.Context, key string, load func(context.Context) (T, error)) (T, error) {
	if b, err := l.store.Get(ctx, key); err == nil {
		var v T
		if json.Unmarshal(b, &v) == nil {
			return v, nil
		}
		// A value we cannot decode (e.g. shape changed) is treated as a miss.
	}

	v, err, _ := l.group.Do(key, func() (any, error) {
		v, err := load(ctx)
		if err != nil {
			return v, err
		}
		if b, err := json.Marshal(v); err == nil {
			_ = l.store.Set(ctx, key, b, l.ttl) // best effort
		}
		return v, nil
	})
	if err != nil {
		var zero T
		return zero, err
	}
	return v.(T), nil
}

// Invalidate deletes keys, ignoring cache failures (the TTL bounds staleness).
func (l *Loader[T]) Invalidate(ctx context.Context, keys ...string) {
	_ = l.store.Delete(ctx, keys...)
}
