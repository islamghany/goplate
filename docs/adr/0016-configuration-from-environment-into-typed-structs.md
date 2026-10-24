# ADR-0016: Load configuration from the environment into typed structs, validated at startup

- **Status:** Accepted
- **Date:** 2026-10-24
- **Deciders:** Islam Mostafa
- **Tags:** ops, config

## Context

Configuration usually ends up in one `Config` struct, which is fine. Two
practices around it cause trouble:

- Passing the **whole** `*config.Config` into handlers, so every module can
  read every secret.
- **Committing** `.env` with real credentials, and hard-coding a database
  URL (password included) in the Makefile.

Neither is a fault of the loading library. They're conventions this decision has to
fix.

## Decision drivers

1. Twelve-factor: configuration comes from the environment; the same image
   runs in every environment.
2. Fail fast: a missing or invalid value stops startup with a clear message.
3. Least privilege: a module sees only its own configuration.
4. Secrets never appear in logs.
5. A widely used, maintained library.

## Considered options

1. **`caarlos0/env` (v11)**: struct tags, defaults, `required`/`notEmpty`,
   prefixes, custom parsers.
2. **`islamghany/enfl`**: a small environment + flags loader.
3. **`knadh/koanf`**: layered sources (defaults < file < env < flags).
4. **`spf13/viper`**.
5. **`kelseyhightower/envconfig`**.

| Option        | ✅                                                      | ❌                                                                |
| ------------- | ------------------------------------------------------ | ----------------------------------------------------------------- |
| caarlos0/env  | Tiny, no dependencies, widely used and maintained; generics; `envPrefix` nesting; `envDefault` | Env only (that's the point); no file layering                    |
| enfl          | Familiar; environment + flags                          | Single maintainer and a small user base, which is a risk for a public template; flags aren't needed in containers |
| koanf         | Flexible layering; many providers                      | More moving parts than an env-only service needs                  |
| viper         | Popular                                                | Heavy dependency tree; global state by default; key-casing surprises |
| envconfig     | Simple                                                 | Unmaintained for years                                            |

## Decision

We use **option 1**:

- `internal/config` defines `Config`, made of per-area sub-structs (`HTTP`,
  `Postgres`, `Redis`, `Auth`, `Jobs`, `OTel`, `RateLimit`, `Note`, ...),
  using `envPrefix` (`POSTGRES_`, `AUTH_`, ...).
- `config.Load(environ map[string]string)` parses, applies defaults, and then
  runs `Validate()` for cross-field rules. For example, the auth secret must
  be at least 32 bytes when using HS256, and `HTTP_TRUSTED_ORIGINS` can't be
  `*` when cookies are enabled. It takes the environment as a map so tests
  don't touch real environment variables. All errors are reported together.
- **Modules receive only their own sub-struct** (`cfg.Note`, `cfg.Auth`),
  never `Config` (ADR-0005).
- **Secrets** use `config.Secret`, a string type whose `String()` and
  `LogValue()` return `"REDACTED"`. At startup we log the effective
  configuration with secrets redacted.
- **`.env` files**:
  - The binary **does not** read `.env`; production gets real environment
    variables.
  - For local development, `make run` and `docker compose` load `.env`.
  - `.env.example` is committed with safe placeholder values and comments.
  - `.env` is gitignored.
- `APP_ENV=development|test|production` only changes **safety defaults**
  (cookie `Secure`, docs UI, log format). It never decides which code paths
  run.

## Consequences

### Positive

- Misconfiguration fails at boot, not at the first request.
- A module can't read another module's secrets.

### Negative / trade-offs we accept

- No YAML config files. If a fork needs them, swapping in koanf is
  contained in `internal/config`.

## Revisit if

- You need hot reload or remote config (Consul, SSM). Introduce koanf behind
  the same `Load` function.
