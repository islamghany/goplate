# ADR-0006: Boilerplate feature scope

- **Status:** Accepted
- **Date:** 2026-10-24
- **Deciders:** Islam Mostafa
- **Tags:** scope

## Context

A boilerplate has two ways to fail. If it does too little, every project
re-solves auth, observability and shutdown, each time slightly differently.
If it does too much, every project first has to delete half of it, and the
parts it doesn't need still cost upkeep. A common mix is a niche feature
such as WebSockets with no health checks, timeouts or metrics: heavy in one
place and missing basics in others.

The bar for inclusion: **almost every production backend needs it on day
one, it's tricky to get right, and it can be removed cleanly** (one
directory plus a few wiring lines, per ADR-0002 and ADR-0005).

## Decision drivers

1. Production readiness of the default build.
2. Every feature can be removed without editing other modules.
3. No infrastructure beyond Postgres and Redis is required to run locally.

## Considered options

Each candidate feature was a separate choice:

| Feature                         | Included? | Decided in |
| ------------------------------- | --------- | ---------- |
| Auth: JWT access + refresh      | ✅ Yes    | ADR-0007, ADR-0008 |
| Cache: Redis + in-memory        | ✅ Yes    | ADR-0009 |
| Background jobs                 | ✅ Yes    | ADR-0010 |
| OpenTelemetry tracing + metrics | ✅ Yes    | ADR-0011 |
| Health and readiness probes     | ✅ Yes    | ADR-0012 |
| Rate limiting                   | ✅ Yes    | ADR-0013 |
| OpenAPI documentation           | ✅ Yes    | ADR-0014 |
| WebSockets hub                  | ❌ Deferred | this ADR |
| GitHub Actions CI               | ❌ Deferred | ADR-0017 |

### Why WebSockets are deferred

- Many backends never need them. The ones that do have very different needs
  (chat rooms, presence, server push, collaborative editing).
- Doing it correctly is a lot of work: a single writer goroutine per
  connection, ping/pong deadlines, backpressure, authentication on upgrade,
  and a pub/sub backplane so messages reach clients connected to other
  instances. A typical first version writes to one gorilla connection from
  several goroutines at once (a data race) and has no keepalive.
- A half-built version is worse than none, because people trust it.

## Decision

Ship the features marked ✅ above, each behind ports so its implementation can
be replaced. Each one follows these rules:

- Its configuration is optional where it reasonably can be. Telemetry
  exporters can be turned off (no-op providers), and the rate limiter can run
  in memory.
- It's wired in `cmd/api` only, and it's registered with the lifecycle manager
  if it runs in the background.
- It's documented in the README with a "how to remove this" note.

WebSockets and CI can be added later through new ADRs. For WebSockets, the
expected shape is a `realtime` module with a `Broker` port (Redis pub/sub
adapter) and a per-connection writer goroutine.

## Consequences

### Positive

- A fresh fork is production-shaped: authenticated, observable, rate-limited,
  documented, and it shuts down gracefully.

### Negative / trade-offs we accept

- More moving parts than a "hello world" template. We offset this with a
  working `docker compose up` and clear removal notes.

## Revisit if

- Most forks delete the same feature. Then it shouldn't be in the default
  build.
- Most forks add the same feature (WebSockets, CI). Then it should be.
