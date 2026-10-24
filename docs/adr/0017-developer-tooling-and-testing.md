# ADR-0017: Developer tooling: Docker Compose, Makefile, golangci-lint and integration tests

- **Status:** Accepted
- **Date:** 2026-10-24
- **Deciders:** Islam Mostafa
- **Tags:** tooling, testing

## Context

A common starting point is a Makefile with hard-coded credentials, no
Docker setup, no linter config and only a handful of tests. A new
contributor can't go from `git clone` to a running, tested server without
asking questions.

## Decision drivers

1. `git clone` to a running API in two commands.
2. Architecture rules (ADR-0002) enforced automatically.
3. Confidence that SQL and transactions actually work against Postgres.
4. Few tools to install.

## Considered options

Each area was decided separately:

**Local infrastructure**

1. **Docker Compose** (Postgres, Redis, Jaeger, Prometheus) ✅
2. Locally installed services ❌ (version drift between machines)
3. Tilt / Skaffold on local Kubernetes ❌ (heavy for a template)

**Task runner**

1. **Make** ✅ (installed almost everywhere; everyone knows it)
2. Taskfile / just ❌ (nicer syntax, but one more tool to install)
3. Mage ❌ (Go-based; verbose for simple tasks)

**Linting**

1. **golangci-lint v2** with a strict, curated config ✅
2. `go vet` + `staticcheck` only ❌ (no `depguard`, no `sloglint`, no
   `errorlint`)

**Database tests**

1. **testcontainers-go** with real Postgres and Redis ✅
2. Mocking pgx ❌ (tests that SQL "was called", not that it works)
3. A shared developer database ❌ (state leaks between runs; doesn't work in parallel)
4. SQLite ❌ (a different SQL dialect)

**CI**: GitHub Actions is **deferred** (not selected). The Makefile targets
are what CI would call, so adding a workflow later is trivial.

## Decision

- **`Dockerfile`**: multi-stage. Build with `golang:1.27` (ADR-0020), then run on
  `gcr.io/distroless/static-debian12:nonroot`. A static binary with
  `-trimpath`, version and commit injected via `-ldflags`. Both binaries
  (`api` and `admin`, per ADR-0002's `cmd/` layout) go into the image, so
  the same image runs the server and the migration job (ADR-0019).
- **`compose.yaml`**: Postgres 17, Redis 7, Jaeger (OTLP receiver and UI),
  Prometheus scraping the admin port. Each has a health check. The API service
  has `depends_on: condition: service_healthy`.
- **Makefile targets**: `up`, `down`, `run`, `migrate`, `seed`, `generate`
  (sqlc), `lint`, `test` (unit tests with `-race`), `test-integration`,
  `cover`, `build`, `docker-build`, `tools`. No credentials in the Makefile;
  it `include`s `.env` if present.
- **golangci-lint**: enabled linters include `errcheck`, `govet`,
  `staticcheck`, `revive`, `errorlint`, `wrapcheck` (only at module
  boundaries), `contextcheck`, `bodyclose`, `sloglint`, `gosec`, `noctx`, and
  `depguard`. The `depguard` rules encode ADR-0002's import table.
- **Tests**:
  - **Unit tests** (default `go test ./...`) cover services with
    hand-written fakes of their own ports, and handlers with `httptest`.
  - **Integration tests** use the `//go:build integration` tag. They start
    Postgres and Redis containers once per package via `TestMain`, apply
    migrations, and isolate each test by giving it its own data (for
    example, a fresh user per test) instead of sharing rows.
  - A small `internal/testutil` package provides fixtures and builders.
- **Tool versions**: sqlc and golangci-lint are installed into `./bin` at
  pinned versions by `make tools`, built with the module's own toolchain.
  They aren't tracked with the `go.mod` `tool` directive: sqlc needs cgo to
  build, and golangci-lint's maintainers advise against it. goose needs no
  CLI at all: `cmd/admin migrate create` uses the goose library.

## Consequences

### Positive

- `make up && make run` gives you a working stack, including traces in Jaeger.
- Boundary violations and context-free logging fail lint.
- SQL is tested against the real engine.

### Negative / trade-offs we accept

- Integration tests need Docker, so they're kept out of the default test run.
- The strict lint config produces some noise at first; exclusions must be
  justified in a comment.

## Revisit if

- The team adopts CI. Add a GitHub Actions workflow that calls
  `make lint test test-integration`, and write an ADR if the pipeline grows
  beyond that.
