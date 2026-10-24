# ADR-0001: Record architecture decisions as ADRs

- **Status:** Accepted
- **Date:** 2026-10-24
- **Deciders:** Islam Mostafa
- **Tags:** process

## Context

goplate is meant to be a starting point that people copy and grow for
years. The most valuable thing a boilerplate can carry isn't the code. It's
the reasoning behind the code. A common failure: a project keeps loose notes
such as `services.md` or `models.md` describing intent, and over time the
code drifts away from them (for example, a models package ends up importing
generated database code even though its notes say models must be
independent of the database). Nobody can tell which side is right, because
the reasons were never written down.

## Decision drivers

- A newcomer should be able to answer "why is it like this?" without asking
  the original author.
- Decisions must be cheap to write, or they won't get written.
- We need a way to change our minds without losing history.

## Considered options

1. **Lightweight ADRs in the repo** (`docs/adr/NNNN-title.md`).
2. **A wiki or Notion page** outside the repo.
3. **Code comments only**.
4. **Nothing**: the code is the documentation.

### Option 1: ADRs in the repo

- ✅ Versioned with the code; reviewed in the same pull request as the change.
- ✅ Immutable history: superseded ADRs stay, so you can see how thinking
  evolved.
- ❌ Requires the discipline to write one per significant decision.

### Option 2: Wiki

- ✅ Rich formatting and comments.
- ❌ Drifts from the code; isn't reviewed with the change; lost when the
  repo is copied as a template.

### Option 3: Code comments only

- ✅ Right next to the code.
- ❌ Comments explain *what* a piece of code does, not *which alternatives were
  rejected*; cross-cutting decisions have no single home.

### Option 4: Nothing

- ❌ This is how code and intent drift apart unnoticed.

## Decision

We keep ADRs in `docs/adr/`, using the format in [template.md](template.md)
(a trimmed-down [MADR](https://adr.github.io/madr/)).

Rules:

- One decision per file, numbered sequentially, never renumbered.
- **Accepted** ADRs are not edited for substance. To change a decision, write
  a new ADR that supersedes the old one, and update the old ADR's status line
  to point to it.
- **Proposed** ADRs are decisions the author made by default that still
  need sign-off. They can be edited freely until accepted.
- A pull request that introduces a new dependency, a new top-level package, or
  a new cross-cutting pattern should include an ADR.
- [README.md](README.md) holds the index and must be updated with every new ADR.

## Consequences

### Positive

- The boilerplate explains itself; forks inherit the reasoning.
- Reviews can point to "this violates ADR-0002" instead of re-arguing a decision
  that was already made.

### Negative / trade-offs we accept

- There's a small writing overhead per decision. We keep the template short to
  make that overhead acceptable.

## Revisit if

- ADRs stop being read or written. If that happens, the format is too heavy.
