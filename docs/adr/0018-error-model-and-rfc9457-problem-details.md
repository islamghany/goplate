# ADR-0018: Transport-agnostic domain errors, rendered as RFC 9457 Problem Details

- **Status:** Accepted
- **Date:** 2026-10-24
- **Deciders:** Islam Mostafa
- **Tags:** api, architecture

## Context

Error handling usually goes wrong in three ways:

- **Handlers choose HTTP status codes inline**
  (`NewError(err, http.StatusUnauthorized)`). Services return plain
  sentinels, and each handler translates them, sometimes inconsistently.
- **The error middleware sends `err.Error()` to clients**, including for 500s
  that wrap raw database errors. That leaks internals such as SQL,
  hostnames, and constraint names.
- **The response shape is ad hoc** (`{"error": "...", "fields": {...}}`),
  with no request ID and nothing machine-readable for the error type.

## Decision drivers

1. Services must not know about HTTP (ADR-0002), but they need to say what
   kind of failure happened.
2. Never leak internal details in 5xx responses.
3. A stable, machine-readable shape that clients can switch on.
4. Every error response can be correlated with logs and traces.

## Considered options

1. **Domain error kinds** (`errs.NotFound`, `errs.Conflict`, ...) mapped to
   status codes in one place, rendered as **RFC 9457
   `application/problem+json`**.
2. **Keep an ad hoc JSON shape** with handler-chosen status codes.
3. **Google API error model** / JSON:API `errors` array.
4. **Services return HTTP-aware errors.**

| Option              | ✅                                                       | ❌                                                       |
| ------------------- | ------------------------------------------------------- | -------------------------------------------------------- |
| Kinds + RFC 9457    | An IETF standard; tooling understands it; extensible; one mapping table | Slightly more verbose bodies                              |
| Ad hoc              | Simple                                                  | Every project reinvents it; inconsistent                  |
| Google / JSON:API   | Rich                                                    | Tied to gRPC or JSON:API conventions we don't otherwise follow |
| HTTP-aware services | Direct                                                  | Couples domain logic to one transport (breaks gRPC, CLI, and job reuse) |

## Decision

We use **option 1**.

- `internal/errs` (a tiny package with no dependencies, importable by every
  module) defines:
  - **`Kind`**: `Invalid`, `Unauthenticated`, `PermissionDenied`, `NotFound`,
    `Conflict`, `PreconditionFailed`, `RateLimited`, `Unavailable`,
    `Internal`
  - **`errs.New(kind, msg)`** for sentinels and **`errs.Wrap(kind, msg, err)`**,
    which keeps the chain for `errors.Is`/`As`
  - **`FieldErrors`**: validation errors per field
- **Modules** define their own sentinels with a kind attached
  (`note.ErrNotFound = errs.New(errs.NotFound, "note not found")`) and
  translate adapter errors at the repository boundary: `pgx.ErrNoRows`
  becomes `ErrNotFound`, and a unique-constraint violation becomes `Conflict`.
- **`internal/web`** owns the only mapping table, from `Kind` to HTTP
  status, and renders:

  ```json
  {
    "type": "https://goplate.dev/problems/not-found",
    "title": "Not Found",
    "status": 404,
    "detail": "note not found",
    "instance": "/v1/notes/0192…",
    "request_id": "01JB…",
    "errors": [{ "field": "title", "message": "title is required" }]
  }
  ```

- For **5xx** responses, `detail` is always generic. The full error chain
  goes only to logs and the trace span, linked by `request_id`.
- **Panics** are recovered at the outermost middleware, logged with the
  stack trace, and rendered as a generic 500, unless the response has
  already started, in which case the connection is aborted.
- Every problem `type` is documented in the OpenAPI spec (ADR-0014).

## Consequences

### Positive

- One place decides status codes; services stay transport-agnostic and
  reusable by job workers.
- No internal details leak.

### Negative / trade-offs we accept

- Adapters must translate driver errors. Forgetting to do so gives a safe
  but unhelpful 500, and a test helper checks the common cases.

## Revisit if

- A second transport (gRPC) is added. Add a `Kind` → gRPC `codes.Code` table
  next to the HTTP one.
