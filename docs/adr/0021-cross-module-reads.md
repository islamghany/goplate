# ADR-0021: Read across modules by composing in the service; JOIN only through published views

- **Status:** Accepted
- **Date:** 2026-10-24
- **Deciders:** Islam Mostafa
- **Tags:** architecture, data

## Context

ADR-0002 gives each module exclusive ownership of its tables and forbids
modules from importing each other. Many screens still need data from two
modules at once. The canonical example is "posts with their author's name",
where posts belong to a `post` module and names to the `user` module.

Without a rule, people reach for the shortcut: a SQL `JOIN` on the other
module's table from inside their own adapter. It works on day one. Over time
it:

- couples modules through private schemas, so renaming a column in `users`
  silently breaks `post`;
- exposes columns the owner never meant to share (emails, password hashes);
- makes extracting a module into its own service impossible without
  rewriting every query that touched its tables.

The opposite mistake is just as common: looking up the author for each row in
a loop, which is N+1 queries per page.

The practical guide with full code is [docs/cross-module-reads.md](../cross-module-reads.md).

## Decision drivers

1. Keep module boundaries real (ADR-0002); they must survive extracting a
   module later.
2. Bounded query count per request (no N+1).
3. Still allow filtering and sorting by another module's data when a
   feature truly needs it.
4. Simple enough that the default is obvious in code review.

## Considered options

1. **Compose in the service**: fetch a page from your own tables, then
   resolve related data in **one batch call** through a port the consumer
   declares (bridged in `cmd/api`).
2. **JOIN another module's tables directly** in your adapter.
3. **JOIN a view the owning module publishes** as its read contract.
4. **Denormalize a snapshot** of the needed fields, refreshed by a job when
   the source changes.
5. **A read-model package** that may read published views of many modules,
   for reports and dashboards.

| Option | ✅ | ❌ |
| --- | --- | --- |
| Compose | Boundaries intact; survives extracting a module; fakes are trivial; two queries per page | Can't filter, sort or paginate by the other module's fields |
| Direct JOIN | One query; easiest to write | Couples private schemas; leaks columns; blocks extraction |
| Published view | One query; filter and sort by foreign fields; the owner controls what's exposed | Shared database and a schema contract to maintain |
| Snapshot | Fastest reads; single-table queries | Sync logic and eventual consistency |
| Read model | Keeps report logic out of modules | One more package; still bound to views |

## Decision

- **Default: option 1.** The consumer declares a batch port in its own terms
  (`post.AuthorDirectory` returning `map[uuid.UUID]Author`); the provider
  exposes a bulk method (`user.Service.ByIDs`, `WHERE id = ANY($1)`); the
  composition root bridges them in `cmd/api/adapters.go`. A per-row lookup is
  a review blocker.
- **When filtering, sorting or paging by foreign data is required: option 3.**
  The owning module creates a view in its own migration (`user_profiles`:
  `id, name`), and the consumer may `JOIN` that view, read-only.
- **Option 4** is allowed for small, slowly changing fields on hot paths.
  The source module enqueues an event job in its write transaction
  (ADR-0010), and the consumer's handler is idempotent.
- **Option 5** is allowed for cross-module reporting, as a query-only package
  that no module imports.
- **Option 2 is not allowed.** A module's SQL never references another
  module's tables. Within one module, joins are unrestricted.

## Consequences

### Positive

- Module boundaries hold for reads as well as writes, so extracting a module
  later only changes bridges and views.
- Query count per request stays constant.
- Reviewers have a short checklist (in the guide) instead of case-by-case
  debates.

### Negative / trade-offs we accept

- Composition costs a second round-trip (sub-millisecond for a primary-key
  `ANY` lookup).
- Published views are contracts: changing them needs the same care as
  changing an API.
- `depguard` can't check SQL, so the "no foreign tables" rule is enforced in
  review, not by tooling.

## Revisit if

- Many features need the same cross-module filters. That suggests the module
  boundary is wrong; consider merging the modules.
- A module is extracted into its own service. Its published views become API
  calls (option 1) or snapshots (option 4).
