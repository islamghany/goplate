# ADR-0014: Spec-first OpenAPI document, embedded and verified against the router

- **Status:** Accepted
- **Date:** 2026-10-24
- **Deciders:** Islam Mostafa
- **Tags:** api, docs

## Context

API consumers (frontend, mobile, partners) need a contract they can read,
mock, and generate clients from. A docs page that just says "use Swagger"
isn't one.

In Go, the generation tools pull in different directions:

- Code-first tools need either comment annotations (which drift) or a
  framework that owns your handlers (which conflicts with ADR-0003).
- Spec-first generators produce server interfaces that also want to own
  routing.

## Decision drivers

1. The contract must stay true; drift should fail CI, not surprise a client.
2. It must not undo ADR-0003 (our own thin `web` layer on `net/http`).
3. Low friction to update.
4. Useful outputs: browsable docs and client generation.

## Considered options

1. **Spec-first, hand-written `openapi.yaml`**, embedded and served, plus a
   **route-drift test**.
2. **Spec-first with `oapi-codegen`** generating server interfaces and
   types.
3. **Code-first with annotations** (`swaggo/swag`).
4. **Code-first through a framework** (huma, fuego) that derives the spec
   from typed handlers.

| Option            | ✅                                                                  | ❌                                                                   |
| ----------------- | ------------------------------------------------------------------ | -------------------------------------------------------------------- |
| Hand-written + test | Contract designed up front; no generator constraints; works with any router | YAML to write; field-level drift is only caught by tests            |
| oapi-codegen      | Generated types match the spec exactly                             | Generated server interfaces dictate handler shape and routing; types leak into the domain if you're not careful |
| swaggo            | Docs next to the code                                              | Annotations are untyped comments and drift silently; tool quality varies |
| huma / fuego      | Spec is always in sync                                             | The framework owns handlers, which contradicts ADR-0003 and ADR-0002   |

## Decision

We use **option 1**:

- `api/openapi.yaml` (OpenAPI 3.1) is the contract. It's embedded with
  `//go:embed` and served at `GET /openapi.yaml`. A docs UI (Scalar, loaded
  from a pinned CDN version) is served at `GET /docs`. Both can be disabled
  with `HTTP_DOCS_ENABLED`, which is off by default in production.
- **Drift test** (`api/openapi_test.go`):
  1. validates the document (`kin-openapi`);
  2. asserts that every route registered in the `web.Router` registry exists
     in the spec, and every spec path exists in the router;
  3. module HTTP tests can run requests and responses through an OpenAPI
     validator, catching field-level drift.
- Error responses reference a shared `Problem` schema (ADR-0018).
- `oapi-codegen` can still be added later in **types-only mode** for
  *clients*. The server side stays hand-written.

## Consequences

### Positive

- The API is designed in the contract first, and CI catches route drift.
- No generator dictates code structure.

### Negative / trade-offs we accept

- Writing YAML by hand. It's mitigated by shared components and the
  validation tests.

## Revisit if

- The API grows past about 100 operations and keeping the hand-written spec in
  sync becomes painful. Re-evaluate oapi-codegen strict server mode behind
  the `web` adapter.
