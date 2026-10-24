# ADR-0020: Target a supported Go release (go 1.26, toolchain 1.27)

- **Status:** Accepted
- **Date:** 2026-10-24
- **Deciders:** Islam Mostafa
- **Tags:** tooling, security

## Context

The Go team supports only the **two most recent** major releases. As of this
ADR's date, the latest release is Go 1.27.3, so 1.26 and 1.27 receive
security fixes and anything older does not. A template meant to be forked
for years shouldn't start out of support, even when an older toolchain
happens to be installed locally.

## Decision drivers

1. Security fixes for the toolchain and standard library.
2. Contributors with an older local Go must still be able to build.
3. Use modern standard library features instead of reimplementing them.

## Considered options

1. **`go 1.26.0` minimum + `toolchain go1.27.3`**, relying on `GOTOOLCHAIN=auto`.
2. **`go 1.27.0` minimum**.
3. **An older release, e.g. Go 1.24**.

| Option             | ✅                                                       | ❌                                               |
| ------------------ | ------------------------------------------------------- | ------------------------------------------------ |
| 1.26 min / 1.27 tc | Both supported releases can build; new clones get the latest patch automatically | Two versions to keep in mind                     |
| 1.27 min           | One version                                             | Forces anyone on 1.26 to upgrade immediately      |
| 1.24               | Works with an older installed toolchain                 | Out of support; misses `http.CrossOriginProtection`, `sync.WaitGroup.Go`, `testing/synctest` |

## Decision

We use **option 1**: `go 1.26.0` and `toolchain go1.27.3` in `go.mod`. An
older local `go` downloads the right toolchain automatically
(`GOTOOLCHAIN=auto`, the default).

What the newer standard library gives us:

- **`http.CrossOriginProtection`** (Go 1.25) provides CSRF protection with
  Fetch Metadata and `Origin` checks (ADR-0008), so we don't hand-write it.
- `sync.WaitGroup.Go`, `testing.T.Context` and `errors.AsType` keep code
  and tests shorter.
- The build image is `golang:1.27` (ADR-0017).

## Consequences

### Positive

- Supported, patched toolchain; less hand-written security code.

### Negative / trade-offs we accept

- The first build on an old machine downloads a toolchain (~70 MB).

## Revisit if

- Every February and August, when a new Go release ships: bump the minimum
  to the older of the two supported releases.
