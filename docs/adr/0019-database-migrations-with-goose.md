# ADR-0019: Database migrations with goose, embedded and run by an explicit admin command

- **Status:** Accepted
- **Date:** 2026-10-24
- **Deciders:** Islam Mostafa
- **Tags:** data, ops

## Context

Migrations commonly go wrong in a few ways: a CLI run from the Makefile
with a database URL (credentials included) baked in; an admin command that
connects through a **second Postgres driver** (`lib/pq`) next to pgx; and
failures that are logged but **not returned**, so a failed migration exits
with code 0 and the deploy carries on.

River (ADR-0010) also ships its own schema migrations that have to be
applied.

## Decision drivers

1. Migrations ship inside the binary (no separate files to deploy).
2. One driver (pgx) for everything.
3. Safe with multiple replicas: never let N pods race to migrate.
4. A failed migration must fail the deploy.
5. Escape hatch for data migrations written in Go.

## Considered options

1. **goose v3**: SQL or Go migrations, `embed.FS`, library API, pgx via
   `database/sql` stdlib.
2. **golang-migrate**.
3. **Atlas**: declarative, schema-as-code with diffing.
4. **tern**: from the pgx author.

| Option         | ✅                                                       | ❌                                                         |
| -------------- | ------------------------------------------------------- | ---------------------------------------------------------- |
| goose          | Simple; embeddable; Go migrations; good library API; no "dirty" lock-in state | Fewer source drivers than golang-migrate (we only need embed) |
| golang-migrate | Widely used; many sources                               | A failed migration leaves a "dirty" flag that needs a manual `force`; separate up/down files; the library API is clunky |
| Atlas          | Diff-based, plans migrations, lints destructive changes  | A bigger concept and tool; some features need a commercial license |
| tern           | Native pgx                                              | Small user base; fewer features                            |

## Decision

We use **option 1**.

- SQL migrations live in `migrations/*.sql` (goose `-- +goose Up/Down`
  annotations), embedded with `//go:embed`. sqlc reads the same files as
  its schema source, so there's **one source of truth** for the schema.
- **`cmd/admin migrate up|down|status|create`** runs goose, then River's
  migrations (`rivermigrate`), using the same pgx pool and a Postgres
  **advisory lock** so concurrent runs are serialized. Any error causes a
  **non-zero exit**.
- **Deploys run migrations as a separate step** (a Kubernetes Job or init
  container, or `make migrate` locally). The API **doesn't** migrate on
  startup by default. `POSTGRES_MIGRATE_ON_START=true` exists for single-instance
  development only.
- Migrations must be **backwards compatible with the previous release**
  (expand/contract), because old pods keep serving during a rolling deploy.
  This is documented in the README.
- `cmd/admin seed` loads development fixtures, idempotently.

## Consequences

### Positive

- One driver, one schema source, and migration failures stop the deploy.

### Negative / trade-offs we accept

- Writing down-migrations is optional; in production, rollbacks go forward
  (write a new migration).

## Revisit if

- Schema changes become frequent and risky. Consider Atlas for
  diff-and-lint.
