# ADR-0002: Organize the code as a modular monolith of domain modules

- **Status:** Accepted
- **Date:** 2026-10-24
- **Deciders:** Islam Mostafa
- **Tags:** architecture, structure

## Context

The goal of goplate is that **every component is replaceable**. Code that uses
a capability depends on an interface, so the implementation behind it (or the
third-party library) can change without touching the callers.

The usual first attempt at that goal is a layer-per-directory structure
(`handlers/`, `services/`, `models/`, `db/`), and it tends to leak in
predictable places:

- Handlers and services end up depending on concrete types (a `*db.Store`,
  a `*services.AuthService`, the global `*config.Config`).
- The few interfaces that do exist are defined by the package that
  *implements* them, and are wider than any single caller needs (a cache
  interface that also exposes `Ping`).
- The models package imports generated database code, so the domain depends
  on the database layer.
- Adding one feature means touching four sibling directories, and nothing
  stops one feature from reaching into another feature's internals.

## Decision drivers

1. Swappability: replacing Postgres, Redis or a library must not ripple into
   business logic.
2. Locality: one feature's code should live in one place.
3. Enforceability: the boundaries should be checked by tooling, not just by
   convention.
4. Room to grow: a module should be extractable into its own service later
   without a rewrite.
5. Low ceremony for small modules.

## Considered options

1. **Domain modules** (package by feature) plus shared infrastructure adapters
   in `internal/platform`.
2. **Classic layers** (`handlers/`, `services/`, `repositories/`, `models/`),
   this time with consumer-side interfaces.
3. **Strict hexagonal** (`core/domain`, `core/ports`, `core/services`,
   `adapters/primary`, `adapters/secondary`).
4. **Microservices from day one**.

### Option 1: Domain modules

- ✅ Everything about "notes" lives under `internal/note`; deleting a feature
  means deleting one directory plus a few wiring lines.
- ✅ Each module declares the interfaces (ports) it needs, which is idiomatic
  Go: *accept interfaces, return structs; define the interface where it's
  used*.
- ✅ Module boundaries match future service boundaries.
- ❌ Shared concerns (DB pool, transactions, logging) need a clear home, which
  is `internal/platform`.

### Option 2: Classic layers

- ✅ Familiar; obvious where a "service" goes.
- ❌ Splits each feature across the whole tree. As the codebase grows, every
  layer directory turns into a junk drawer.
- ❌ Nothing structural stops `services/note.go` from calling
  `repositories/user.go` directly.

### Option 3: Strict hexagonal

- ✅ The most explicit boundaries.
- ❌ It's still organized by technical role at the top level, so it inherits
  the locality problem of option 2.
- ❌ Heavy ceremony (separate `ports` package, DTO mapping at every hop) for
  modules that are basically CRUD.

### Option 4: Microservices

- ❌ Distributed transactions, network failure modes, deployment and
  observability overhead, all before there's a product. A well-bounded
  monolith keeps the option open for later.

## Decision

We use **option 1**. The layout:

```
cmd/
  api/                 composition root: config → adapters → modules → lifecycle
  admin/               one-off commands (migrate, seed)
internal/
  <module>/            e.g. user, auth, note
    <module>.go        domain types, domain errors, and the PORTS this module needs
    service.go         business logic; depends only on its own ports
    http.go            transport: routes, request/response DTOs, mapping
    <module>pg/        Postgres adapter implementing this module's repository port
  platform/            infrastructure adapters shared by modules
    postgres/          pool, transaction manager
    redis/             client, cache adapter, rate-limit adapter
    jobs/              River client and worker registry
    otel/ ...
  web/                 HTTP toolkit (router, middleware, encode/decode, errors)
```

Dependency rules (enforced with `depguard` in golangci-lint, see ADR-0017):

| Package                  | May import                                                  | Must not import                          |
| ------------------------ | ----------------------------------------------------------- | ---------------------------------------- |
| `internal/<m>` (core)    | stdlib, small pure libraries (uuid), `internal/errs`        | `platform/*`, `web`, drivers, other modules |
| `internal/<m>` `http.go` | its own core, `internal/web`                                | `platform/*`, drivers                    |
| `internal/<m>/<m>pg`     | its own core, `platform/postgres`, generated sqlc code      | other modules, `web`                     |
| `internal/platform/*`    | third-party drivers/SDKs                                    | any domain module                        |
| `cmd/*`                  | everything (this is the only place that does)               | n/a                                      |

Rules for interfaces:

- **The consumer defines the interface.** `auth` needs to look up users, so
  `auth` declares `type UserFinder interface { FindByEmail(...) }`, and
  `user.Service` happens to satisfy it. `auth` never imports `user`.
- **Keep interfaces small.** An interface lists only the methods its
  consumer calls. Health checks are a separate `Checker` interface, never a
  method on a domain port.
- **Constructors return concrete types.** The caller decides what to abstract.

## Consequences

### Positive

- Swapping Redis for Valkey or Memcached, or Postgres for anything else, only
  touches `platform/` and `cmd/api`.
- Each module can be unit-tested with hand-written fakes of its own small
  ports; no mocking framework is required.
- Linting catches boundary violations during review instead of months later.

### Negative / trade-offs we accept

- Some duplication is intentional: two modules may declare near-identical small
  interfaces. That's the price of decoupling, and it's cheap.
- Cross-module workflows have to be orchestrated explicitly, either in the
  calling module's service or by publishing a job (ADR-0010). Modules don't
  call each other directly.

## Revisit if

- One module needs to scale or deploy independently. Extract it into its own
  service; its ports become network clients.
- The `depguard` rules accumulate many exceptions. That means the boundaries
  are in the wrong place.
