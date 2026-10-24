# ADR-0003: Use the standard library router behind a thin internal `web` package

- **Status:** Accepted
- **Date:** 2026-10-24
- **Deciders:** Islam Mostafa
- **Tags:** architecture, http

## Context

Since Go 1.22, `http.ServeMux` supports method matching and path wildcards
(`GET /v1/notes/{id}`). It also returns 405 for a wrong method and 404 for an
unknown path. That covers most of what routers like chi or gorilla/mux
used to be needed for.

Wrapping `ServeMux` in a small in-house toolkit is a sound idea, but three
mistakes are common:

- Middleware with a custom signature (`func(Handler) Handler` over
  error-returning handlers), so the ecosystem's standard middleware
  (`otelhttp`, `http.TimeoutHandler`, CORS libraries) can't be plugged in.
- Tracking the response status by mutating a struct stored in `context`,
  instead of wrapping `http.ResponseWriter`.
- A `:id` → `{id}` path translation layer: extra code that obscures the
  standard syntax.

## Decision drivers

1. Replaceability: handlers must not depend on a framework's context type.
2. Compatibility with standard `net/http` middleware.
3. Minimal dependencies for a long-lived boilerplate.
4. Keep the ergonomics of error-returning handlers, so error responses are
   produced in exactly one place.

## Considered options

1. **stdlib `ServeMux`** wrapped by an internal `web` package.
2. **chi**: small, `net/http`-compatible router with route groups.
3. **Echo / Gin**: full frameworks with their own context type.
4. **Fiber**: built on fasthttp.

### Option 1: stdlib + internal `web`

- ✅ Zero dependencies; maintained by the Go team; gets faster with every
  release.
- ✅ Handlers are `http.Handler`, so any router can replace this one later.
- ❌ No built-in route groups or per-group middleware. We write about 150
  lines to add them.
- ❌ No regex constraints on path parameters; values are parsed in the
  handler.

### Option 2: chi

- ✅ Mature groups, sub-routers, and a middleware collection.
- ❌ Since Go 1.22, most of what it offers on top of the stdlib is grouping,
  which isn't worth a dependency.

### Option 3: Echo / Gin

- ✅ Batteries included (binding, validation, rendering).
- ❌ `echo.Context` / `gin.Context` spreads through every handler, which is
  the opposite of replaceable.
- ❌ Their own middleware ecosystems, not `net/http`'s.

### Option 4: Fiber

- ❌ fasthttp isn't `net/http`: no `http.Handler`, no HTTP/2, different
  context semantics, and many standard libraries (including `otelhttp`) don't
  apply.

## Decision

We use **option 1**. The `internal/web` package provides:

- `type HandlerFunc func(w http.ResponseWriter, r *http.Request) error`.
  Handlers return errors, and an adapter turns a `HandlerFunc` into an
  `http.Handler` that sends any error to **one** central error encoder
  (ADR-0018).
- **Middleware is the standard `func(http.Handler) http.Handler`**, so
  third-party middleware works without adapters.
- `Router` with `Group(prefix, mw...)` and `Handle(pattern, h, mw...)`,
  using native ServeMux patterns (`"GET /notes/{id}"`). We don't translate
  path syntax.
- A `ResponseWriter` wrapper that records the status code and bytes written,
  for logging and metrics.
- Typed helpers: `web.Decode[T]` (size-limited, strict JSON, then validation),
  `web.JSON`, `web.NoContent`, `web.PathUUID`, and pagination and query helpers.

Handlers never touch `ServeMux` directly. To switch to chi or anything else,
we change one file in `internal/web`.

## Consequences

### Positive

- No framework lock-in; full compatibility with the `net/http` ecosystem.
- Error handling lives in one place, so a handler can't forget to log or
  sanitize an error.

### Negative / trade-offs we accept

- We own and test a small routing helper.
- Developers coming from Gin or Echo will miss `c.Bind()`. The `web.Decode`
  helper gives them the equivalent.

## Revisit if

- We need features ServeMux lacks and our wrapper would have to reimplement a
  real router: complex constraints, or route introspection beyond what our
  registry keeps.
