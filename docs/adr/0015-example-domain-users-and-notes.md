# ADR-0015: Ship a generic example domain, users/auth plus a "notes" resource

- **Status:** Accepted
- **Date:** 2026-10-24
- **Deciders:** Islam Mostafa
- **Tags:** scope

## Context

A boilerplate teaches by example. Developers copy the first module they see,
so that module defines the standard for everything after it. A
product-specific domain (for example, customers and business owners) has to
be deleted by every fork, and half-finished stub handlers teach the wrong
things. A boilerplate needs one complete example to copy.

## Decision drivers

1. Show **every pattern** once, end to end:
   - routes, DTOs, validation
   - service logic and authorization
   - repository and transactions
   - cache
   - jobs
   - pagination
   - tests at every layer
2. Easy to delete or rename when a project starts.
3. Neutral: no business assumptions.

## Considered options

1. **Generic**: `user` + `auth` modules, plus one sample CRUD module
   (`note`).
2. **A product-specific domain** (users, customers, business owners).
3. **Users and auth only**.

| Option            | ✅                                                    | ❌                                                         |
| ----------------- | ---------------------------------------------------- | ---------------------------------------------------------- |
| Users + notes     | Complete reference pattern; trivially deletable      | One throwaway module                                       |
| Product domain    | Realistic multi-role example                         | Product-specific; every fork has to delete it              |
| Users/auth only   | Minimal                                              | Auth is a special case; doesn't show plain CRUD, pagination, ownership or jobs |

## Decision

We use **option 1**:

- **`user`**: registration, profile (`GET/PATCH /v1/users/me`), roles
  (`user`, `admin`). An admin-only listing shows a role check inside the service.
- **`auth`**: login, refresh, logout, logout-all (ADR-0007, ADR-0008).
  Depends on a `UserFinder` port, not on the `user` package.
- **`note`**: CRUD on `/v1/notes`, demonstrating:
  - **ownership authorization** in the service (a user can only see and change
    their own notes; this is checked in the service, not the handler)
  - **cursor pagination** (`?limit=&cursor=`) with an opaque cursor encoding
    `(created_at, id)`, which is stable under concurrent inserts, unlike
    `OFFSET`
  - **filtering** (`?q=`, `?tag=`) using sqlc nullable parameters
  - **optimistic concurrency** via a `version` column and `If-Match` / 412
  - **cache-aside** on `GET /v1/notes/{id}` with invalidation on write
  - **transactional job**: creating a note queues a `note.created` job in the
    same transaction (ADR-0010); its idempotent worker logs and records
    metrics
  - **tests**: service unit tests with fakes, HTTP handler tests, and
    `notepg` integration tests against real Postgres (ADR-0017)
- IDs are **UUIDv7**: time-ordered (index-friendly like a serial), not
  guessable, and safe to expose.
- A `docs/adding-a-module.md` guide walks through copying `note` into a new
  module.

## Consequences

### Positive

- New modules start from a complete, tested template.

### Negative / trade-offs we accept

- Forks have to delete `note` once they have their own modules. The guide
  lists the files and wiring lines.

## Revisit if

- The `note` module stops reflecting current conventions. It's the reference,
  so it must be updated first whenever a convention changes.
