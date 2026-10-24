# ADR-0005: Wire dependencies manually in a single composition root

- **Status:** Accepted
- **Date:** 2026-10-24
- **Deciders:** Islam Mostafa
- **Tags:** architecture

## Context

ADR-0002 makes every module depend on small interfaces. Something has to
create the concrete implementations and pass them in. This is often done
inside a `Run`/`NewServer` function, mixed together with signal handling,
server startup and route registration, and handlers receive whole
infrastructure objects (`*config.Config`, `*db.Store`) instead of what they
actually need.

## Decision drivers

1. Wiring mistakes should fail at **compile time**.
2. Wiring should be readable top to bottom by someone new to the codebase.
3. No reflection or magic in the startup path.
4. Wiring should scale to around 20 modules without becoming unmanageable.

## Considered options

1. **Manual wiring** in `cmd/api`, using plain constructors.
2. **google/wire**: compile-time code generation.
3. **uber/fx** (dig): runtime reflection-based container with lifecycle
   hooks.
4. **Global singletons / service locator**.

### Option 1: Manual wiring

- ✅ It's just Go: jump-to-definition works, and the compiler checks
  everything.
- ✅ Startup order is explicit and visible.
- ❌ The wiring file grows linearly with the number of modules.

### Option 2: wire

- ✅ Removes the boilerplate in large graphs; still checked at compile time.
- ❌ An extra tool and generated code; error messages are confusing; the
  project is in maintenance mode.

### Option 3: fx

- ✅ Lifecycle hooks and modules out of the box.
- ❌ Errors appear at runtime, at startup; reflection; a graph that's hard
  to follow ("where does this come from?"). It also adds a framework to
  every constructor.

### Option 4: Globals

- ❌ Hidden dependencies, untestable, initialization-order bugs (for example,
  a validator configured through `init()` globals).

## Decision

We use **option 1**, structured so it stays small:

- `cmd/api/main.go` only calls `run(ctx, os.Args, os.Getenv, os.Stdout)` and
  exits with its error code. This makes the whole program runnable from tests.
- `run` follows a fixed sequence: **load config → build logger and telemetry →
  build platform adapters → build modules → register components with the
  lifecycle manager (ADR-0012) → run**.
- Each module exposes one constructor that takes a `Deps` struct of
  interfaces and a module-specific `Config`, and returns a `*Module`:

  ```go
  notes := note.NewModule(note.Deps{
      Repo:  notepg.NewRepository(txm),
      Tx:    txm,
      Jobs:  queue,
      Cache: cacheStore,
      Log:   log,
  }, note.Config{CacheTTL: cfg.Note.CacheTTL})
  notes.Routes(protected) // a router group carrying the auth middleware
  ```

- Modules receive **their own config struct**, never the global `Config`.
- No package-level mutable state anywhere, including the validator and the
  logger.

## Consequences

### Positive

- The dependency graph is a single readable file.
- Tests build a module with fakes using the same constructor.

### Negative / trade-offs we accept

- Adding a module means a few lines in `cmd/api`. That's deliberate: it's
  the one place where you can see what the app is made of.

## Revisit if

- The composition root goes past roughly 300 lines and splitting it by module
  no longer helps. Then reconsider `wire`.
