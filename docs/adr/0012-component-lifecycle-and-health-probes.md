# ADR-0012: A component lifecycle manager drives startup, health probes and graceful shutdown

- **Status:** Accepted
- **Date:** 2026-10-24
- **Deciders:** Islam Mostafa
- **Tags:** ops, architecture

## Context

Startup and shutdown are often handled by hand, for one thing only, the
HTTP server:

- Signal handling lives deep inside a `Run` function.
- The database is closed with `defer`, possibly *before* in-flight requests
  have finished.
- `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout` and `IdleTimeout` are
  left unset, which leaves the server open to slowloris attacks.
- There are no health endpoints, so an orchestrator can't tell "alive" from
  "ready".

A real service has several long-running parts: the public HTTP server, the
admin HTTP server, River workers, and telemetry exporters that must flush on
exit. Each needs the same treatment.

## Decision drivers

1. Ordered startup and **reverse-ordered** shutdown (stop taking traffic,
   drain, then close dependencies).
2. One failure stops everything cleanly.
3. Kubernetes-correct probes: liveness must not depend on external services
   (or a DB blip triggers a restart storm), while readiness must.
4. Adding a component should take one line.

## Considered options

1. **A small internal `lifecycle` package** with `Component` and `Checker`
   interfaces.
2. **Ad-hoc goroutines in `main`**.
3. **`errgroup` alone**.
4. **`oklog/run`**.
5. **`uber/fx` lifecycle hooks**.

| Option        | ✅                                                    | ❌                                                         |
| ------------- | ---------------------------------------------------- | ---------------------------------------------------------- |
| lifecycle pkg | Exactly the semantics we need; about 150 lines; integrates health checks | We own it                                                  |
| Ad hoc        | Nothing to learn                                     | Every new component re-implements shutdown, and some get it wrong |
| errgroup      | Standard; cancels on first error                     | No ordering; no shutdown deadline per component; no health integration |
| oklog/run     | Proven actor pattern                                 | Unmaintained; no ordering or health                        |
| fx            | Mature hooks                                         | Brings in the whole DI framework that ADR-0005 rejected    |

## Decision

We use **option 1**:

```go
package lifecycle

type Component interface {
    Name() string
    Start(ctx context.Context) error // block until ctx is done, or return quickly if it starts background work
    Stop(ctx context.Context) error  // ctx carries the shutdown deadline
}

type Checker interface { // optional, for readiness
    Name() string
    Check(ctx context.Context) error
}
```

**Startup**

1. `main` creates the root context with
   `signal.NotifyContext(ctx, SIGINT, SIGTERM)`.
2. Dependencies are dialed and pinged **before** any server listens, so we
   fail fast with a clear error.
3. Components start in registration order. If any `Start` fails, everything
   already started is stopped in reverse order.

**Shutdown** (on a signal or a component failure)

1. Readiness flips to failing at once, so `/readyz` returns 503.
2. Wait `SHUTDOWN_DRAIN_DELAY` (default 5s), so load balancers notice and
   stop routing new requests.
3. Stop components in **reverse** order: public HTTP (`Server.Shutdown`
   drains in-flight requests), then River workers (finish running jobs),
   then the admin server, then telemetry flush, then pools are closed.
4. The whole sequence is bounded by `SHUTDOWN_TIMEOUT` (default 20s). That
   must stay below Kubernetes' `terminationGracePeriodSeconds` (30s by
   default). If the deadline is hit, connections are force-closed and the
   process exits non-zero.

**Probes** (served on both the public and admin ports)

- `GET /livez`: returns 200 if the process is running and the lifecycle
  loop is responsive. It **doesn't check dependencies**.
- `GET /readyz`: runs every registered `Checker` in parallel, each with a
  short timeout, and caches the result for about a second so probes can't
  overload the database. Returns 503 with per-check detail (detail only on
  the admin port) when any check fails or shutdown has begun.

**HTTP server defaults** (all configurable): `ReadHeaderTimeout` 5s,
`ReadTimeout` 15s, `WriteTimeout` 30s, `IdleTimeout` 120s,
`MaxHeaderBytes` 1 MiB. Request bodies are limited by `web.Decode` (ADR-0003).

## Consequences

### Positive

- Zero-downtime rolling deploys work correctly.
- Adding a worker or server takes one `Register` line.

### Negative / trade-offs we accept

- One more internal package to maintain and test.
- `WriteTimeout` cuts off long streaming responses. Those routes need their
  own server or `http.ResponseController` deadlines.

## Revisit if

- We add long-lived connections (WebSockets, SSE). They need their own drain
  strategy (notify clients, then close), registered as their own component.
