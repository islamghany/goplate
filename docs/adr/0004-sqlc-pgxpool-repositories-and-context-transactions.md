# ADR-0004: Use sqlc + pgxpool behind module repositories, with context-carried transactions

- **Status:** Accepted
- **Date:** 2026-10-24
- **Deciders:** Islam Mostafa
- **Tags:** data, architecture

## Context

sqlc with pgx is a good base, but three mistakes are easy to make on top of
it:

- **Opening a single `*pgx.Conn`.** A single connection isn't safe for
  concurrent use, so two simultaneous requests can corrupt each other. A
  server needs a pool.
- **A leaky transaction abstraction.** A `Store.ExecTx(fn func(*Queries) error)`
  hands generated sqlc types to callers, and it's easy for code inside the
  callback to keep using the non-transactional store, so the "transaction"
  doesn't actually cover its queries.
- **A domain that depends on the database.** Converting generated structs
  (with `pgtype.Text` and similar) inside the domain package makes the domain
  import sqlc.

Services sometimes need atomicity across several repositories, and across a
repository and the job queue (ADR-0010), *without* knowing that Postgres
exists.

## Decision drivers

1. Compile-time safety for SQL, and SQL that stays readable.
2. Business logic must not import pgx, sqlc output, or `database/sql`.
3. Atomic operations across repositories and the job queue.
4. Performance and Postgres feature access (COPY, LISTEN/NOTIFY, arrays, JSONB).

## Considered options

Query layer:

1. **sqlc** generating code against **pgx/v5**, used through a **pgxpool**.
2. **Hand-written SQL on pgx** (`pgx.CollectRows`, `RowToStructByName`).
3. **GORM**.
4. **ent**.
5. **sqlx + squirrel** (query builder).

Transaction style:

- **A. Context-carried transaction**: `tx.WithinTx(ctx, func(ctx) error)`.
  Repositories pick up the transaction from `ctx` when one is present.
- **B. Explicit unit of work**: `uow.Do(ctx, func(r Repos) error)` hands
  transaction-bound repositories to the callback.
- **C. Shared store**: expose generated `*Queries` to callers.

### Query layer

| Option        | ✅                                                       | ❌                                                                 |
| ------------- | ------------------------------------------------------- | ------------------------------------------------------------------ |
| sqlc + pgx    | SQL is the source of truth; generated, type-checked code; zero reflection; full Postgres feature set | Codegen step; dynamic queries (optional filters) are awkward       |
| Hand-written  | No codegen; full control                                | Column/field mismatches are only caught at runtime; lots of boilerplate |
| GORM          | Fast CRUD; auto-migrations                              | Reflection, hidden queries, N+1 traps, weak typing; hard to reason about performance |
| ent           | Typed graph API; schema as code                         | Heavy codegen; the ORM's schema owns the database; steep learning curve |
| sqlx+squirrel | Good for dynamic queries                                | `database/sql` interface loses pgx-native features; no compile-time checks |

### Transaction style

| Option | ✅                                                                       | ❌                                                                             |
| ------ | ----------------------------------------------------------------------- | ------------------------------------------------------------------------------ |
| A      | Service signatures stay unchanged; works across modules and the job queue; one small interface (`TxManager`) | Implicit: a repository *must* get its querier from `ctx`, or it silently runs outside the transaction |
| B      | Explicit: you can see which repositories are in the transaction          | The unit of work must know every repository type, which couples modules; it gets big |
| C      | Simple                                                                  | Leaks the persistence layer into business code                                  |

## Decision

**Query layer: sqlc + pgx/v5 + pgxpool.**

- Each module keeps its SQL in `internal/<m>/<m>pg/queries.sql`. sqlc
  generates code into that adapter package only.
- The adapter implements the module's repository port and **maps sqlc rows
  to domain types**. Nothing outside `<m>pg` sees generated types.
- `pgxpool.Pool` settings (max/min connections, lifetime, idle time,
  health check period) come from config (ADR-0016). Pool statistics are
  exported as metrics (ADR-0011).
- For genuinely dynamic queries (multi-field filters), we use sqlc with
  `sqlc.narg()` nullable parameters first, and fall back to hand-written pgx
  inside the adapter. We don't add a query builder.

**Transactions: option A**, made safe by construction:

```go
// internal/platform/postgres
type TxManager struct{ pool *pgxpool.Pool }
func (m *TxManager) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error

// DBTX returns the transaction stored in ctx, or the pool if there is none.
// Every adapter calls this; adapters never hold a *pgxpool.Pool directly.
func (m *TxManager) DBTX(ctx context.Context) DBTX
```

Each module declares only what it needs:

```go
// internal/note/note.go
type Transactor interface {
    WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
}
```

- Nested `WithinTx` calls reuse the outer transaction (they don't nest
  savepoints by default).
- Repository adapters are constructed with the `TxManager`, not the pool,
  so there is no way to accidentally bypass the transaction.
- The River adapter (ADR-0010) reads the same transaction from `ctx`, so
  "insert row + enqueue job" is atomic.

## Consequences

### Positive

- Business logic is testable with in-memory fakes and a no-op `Transactor`.
- A pool makes concurrent requests safe; a single connection is not.
- SQL stays reviewable as SQL.

### Negative / trade-offs we accept

- `make generate` must run after SQL changes; CI should check that generated
  code is up to date.
- Context-carried transactions are implicit. We mitigate this by giving
  adapters only the `TxManager` (never the pool) and by covering transactions
  in integration tests (ADR-0017).
- A transaction must not span slow external calls (HTTP, email). Those go
  through the job queue after commit.

## Revisit if

- We need a non-Postgres primary store. Only the `<m>pg` adapters and
  `platform/postgres` change.
- Dynamic query needs outgrow `sqlc.narg`. Consider a builder inside the
  adapters only.
