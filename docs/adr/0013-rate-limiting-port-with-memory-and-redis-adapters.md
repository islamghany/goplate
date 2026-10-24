# ADR-0013: Rate limiting behind a port, with in-memory and Redis (GCRA) adapters

- **Status:** Accepted
- **Date:** 2026-10-24
- **Deciders:** Islam Mostafa
- **Tags:** security, ops

## Context

Without rate limiting, one misbehaving client can exhaust the database pool,
and `/auth/login` is open to credential stuffing.

Limits have to be enforced across all instances behind a load balancer.
In-memory counters let a client get N× the limit with N replicas.

## Decision drivers

1. Correct limits across instances in production.
2. Zero infrastructure in development and tests.
3. Different policies per route (strict for login, relaxed for reads).
4. Correct client identification behind proxies.

## Considered options

1. **A `Limiter` port** with an **in-memory** adapter
   (`golang.org/x/time/rate`) and a **Redis** adapter (GCRA via
   `go-redis/redis_rate`).
2. **In-memory only**.
3. **Redis only**.
4. **At the edge only** (NGINX, Envoy, API gateway, Cloudflare).

| Option        | ✅                                                       | ❌                                                         |
| ------------- | ------------------------------------------------------- | ---------------------------------------------------------- |
| Port + both   | Right tool per environment; easy to test                | Two adapters to maintain                                   |
| Memory only   | No infrastructure; fastest                              | Wrong in multi-replica deployments                         |
| Redis only    | Always correct                                          | Local runs and tests need Redis                            |
| Edge only     | Stops abuse before it reaches the app                   | The edge doesn't know the authenticated user or business rules; not portable across hosting setups |

## Decision

We use **option 1**:

```go
type Limiter interface {
    Allow(ctx context.Context, key string, policy Policy) (Decision, error)
}
type Policy struct { Name string; Rate int; Period time.Duration; Burst int }
type Decision struct { Allowed bool; Remaining int; RetryAfter, ResetAfter time.Duration }
```

- **GCRA** (generic cell rate algorithm) in Redis, run as an atomic Lua
  script by `redis_rate`. It gives smooth limiting with bursts, without the
  spikes at window boundaries that fixed windows have.
- `RATELIMIT_DRIVER=redis|memory`.
- **Middleware**: `ratelimit.Middleware(limiter, policy, keyFunc)`.
  - The default key is the user ID for authenticated requests, otherwise the
    client IP.
  - Default policies: global `100/min` per key; `POST /v1/auth/login` limited
    to `5/min` per IP **and** `10/hour` per email.
- **Client IP**: `X-Forwarded-For` / `Forwarded` are trusted **only** when
  the direct peer is in `HTTP_TRUSTED_PROXIES` (CIDRs). Otherwise
  `RemoteAddr` is used. Blindly trusting XFF would let anyone bypass limits.
- **Responses**: `429` with `Retry-After`, and the IETF `RateLimit-Policy` /
  `RateLimit` headers.
- **Failure policy**: if Redis is unavailable, we **fail open** by default,
  logging and counting each failure, so a Redis incident doesn't become a full
  outage. Login routes can be configured to **fail closed**.

## Consequences

### Positive

- Brute-force protection on auth from day one.
- Policies are code, reviewed with the routes they protect.

### Negative / trade-offs we accept

- Each request that's rate-limited costs one Redis round-trip (sub-millisecond
  inside the same network).
- Trusted-proxy configuration has to be kept in sync with the infrastructure.

## Revisit if

- An edge or gateway limiter is introduced. Keep the app limiter for per-user
  and business limits only.
