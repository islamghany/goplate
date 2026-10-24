# ADR-0009: Cache behind a consumer-defined port, with Redis and in-memory adapters

- **Status:** Accepted
- **Date:** 2026-10-24
- **Deciders:** Islam Mostafa
- **Tags:** data, performance

## Context

A cache interface is easy to get subtly wrong: defined next to its Redis
implementation instead of by its consumers, including infrastructure
concerns such as `Ping`, and accepting only `string` values. Note also that
`github.com/go-redis/redis/v8` is deprecated in favor of
`github.com/redis/go-redis/v9`.

## Decision drivers

1. Business code must not know which cache backend is in use (ADR-0002).
2. Tests and local runs shouldn't require Redis.
3. A cache outage must not take the API down.
4. Shared state across multiple instances.

## Considered options

1. **Redis (go-redis v9) + an in-memory adapter**, behind a small port.
2. **Valkey**: a Redis fork that speaks the same protocol (works with the
   same adapter).
3. **Memcached**.
4. **In-process only** (otter, ristretto).
5. **No cache** until it's needed.

| Option            | ✅                                                     | ❌                                                     |
| ----------------- | ----------------------------------------------------- | ------------------------------------------------------ |
| Redis + in-memory | Shared across instances; also backs rate limiting and the token denylist; the in-memory adapter makes tests quick | One more piece of infrastructure in production          |
| Valkey            | Open-source license; same protocol                    | No code change; it's a deployment choice, not a code choice |
| Memcached         | Simple and fast                                       | No TTL per data structure, no Lua, can't back rate limiting |
| In-process only   | Zero infrastructure; fastest                          | Every instance has its own copy; invalidation across instances is impossible |
| No cache          | Simplest                                              | Rate limiting and the denylist still need shared state  |

## Decision

We use **option 1**.

- **Consumers declare what they need.** For example, the `note` module
  declares:

  ```go
  type Cache interface {
      Get(ctx context.Context, key string) ([]byte, error) // returns cache.ErrMiss on a miss
      Set(ctx context.Context, key string, val []byte, ttl time.Duration) error
      Delete(ctx context.Context, keys ...string) error
  }
  ```

  Values are `[]byte`. Serialization belongs to the caller. A small generic
  helper, `cache.Loader[T]` (`GetOrLoad`/`Invalidate`), implements
  cache-aside with JSON encoding and `singleflight` so concurrent misses for the same key trigger only one load.
- **Adapters** live in `internal/platform/redis` (go-redis v9) and
  `internal/platform/memory` (a map with TTL, for tests and local
  development). Redis readiness is a separate `lifecycle.Checker` registered
  in `cmd/api` (ADR-0012), never a method on the cache port.
- **Keys** are namespaced: `<app>:<module>:<version>:<id>`. Bumping the
  version invalidates a whole shape of cached data.
- **Failure policy**: reads *fail open*. A Redis error is logged, counted,
  and treated as a cache miss. Writes are best-effort. The cache is never
  the source of truth.
- `CACHE_DRIVER=redis|memory` selects the adapter.

## Consequences

### Positive

- Business code doesn't know about Redis; tests run without it.
- Moving to Valkey or a managed Redis service means changing only the
  connection string.

### Negative / trade-offs we accept

- The caller is responsible for invalidation. Each module documents which
  writes invalidate which keys.

## Revisit if

- Cache hit rates are low (shown by metrics). Then remove the caching from
  that path; it's only adding complexity.
