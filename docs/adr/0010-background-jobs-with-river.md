# ADR-0010: Run background jobs on Postgres with River

- **Status:** Accepted
- **Date:** 2026-10-24
- **Deciders:** Islam Mostafa
- **Tags:** architecture, async

## Context

Many operations shouldn't happen inside the HTTP request: sending email,
calling webhooks, generating thumbnails, cleaning up expired sessions,
fanning work out to other modules. The classic bug is **dual writes**:

```
BEGIN; INSERT note; COMMIT;   -- succeeds
queue.Publish("note.created") -- fails, or the process dies → the job is lost
```

or the reverse, where the job is published but the transaction rolls back,
leaving a phantom job for data that doesn't exist. The usual fix is a
transactional outbox. A Postgres-backed queue gives you that outbox
without writing one.

## Decision drivers

1. Atomicity between business writes and queuing a job.
2. No new infrastructure (Postgres and Redis are already required).
3. Retries with backoff, scheduled and periodic jobs, uniqueness, and
   graceful drain on shutdown.
4. Replaceable behind a port.

## Considered options

1. **River** (`riverqueue/river`): a Postgres queue built on pgx v5.
2. **Asynq**: a Redis-backed queue.
3. **Hand-rolled queue** using `SELECT … FOR UPDATE SKIP LOCKED`.
4. **Temporal**: a durable workflow engine.
5. **A message broker** (NATS JetStream, Kafka, SQS) with an outbox.

| Option       | ✅                                                                                         | ❌                                                                                     |
| ------------ | ----------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------- |
| River        | `InsertTx` queues the job in *our* transaction; uses the pgx we already have; retries, cron, unique jobs, typed args via generics; web UI available | Adds load to Postgres; its own schema migrations; MPL-2.0 license (fine for most uses; some extras are in the commercial River Pro) |
| Asynq        | Mature; fast; has a UI                                                                     | Redis and Postgres can't share a transaction, so the dual-write problem returns       |
| Hand-rolled  | No dependency                                                                              | Retries, backoff, leasing, stuck-job rescue, cron and metrics are hard to get right and take months to harden |
| Temporal     | Durable multi-step workflows, sagas                                                        | A whole cluster to operate; overkill for a boilerplate                                 |
| Broker+outbox| Cross-service events; high throughput                                                      | Two systems plus an outbox relay to build and run                                      |

## Decision

We use **option 1, River**, behind a port:

```go
// declared by the consumer, e.g. internal/note
type JobEnqueuer interface {
    Enqueue(ctx context.Context, job jobs.Job) error
}
```

- The adapter in `internal/platform/jobs` calls River's `InsertTx` with the
  transaction from `ctx` when there is one (ADR-0004), and plain `Insert`
  otherwise. **A job queued inside `WithinTx` is committed or rolled back
  together with the business data.**
- Job argument types and workers live in the module that owns them
  (`note.Created` and `note.Service.HandleCreated`). Handlers are plain
  functions on the module's service. They hold no
  business logic of their own.
- The worker registry is assembled in `cmd/api`. Whether this process works
  jobs is controlled by config (`JOBS_WORKER_ENABLED`), so a single binary can
  run as API only, worker only, or both.
- The River client is a lifecycle component (ADR-0012). On shutdown it stops
  fetching new jobs and gives running jobs time to finish, up to the shutdown
  budget.
- River's schema migrations are applied by `cmd/admin migrate`, in the same
  step as our own (ADR-0019).
- Job metrics and traces go through OTel (ADR-0011).
- Built-in periodic job: purge expired auth sessions (ADR-0007).

## Consequences

### Positive

- No lost or phantom jobs, with no outbox code to write.
- No extra infrastructure.

### Negative / trade-offs we accept

- Postgres carries the queue's load. That's fine up to thousands of jobs per
  second on decent hardware, but needs watching.
- Jobs are **at-least-once**, so workers must be idempotent. This is
  documented and shown in the example worker.

## Revisit if

- Job throughput competes with OLTP load. Move River to a separate database,
  or move high-volume jobs to a broker.
- You need events consumed by *other services*. Add a broker, using River as
  the outbox relay.
