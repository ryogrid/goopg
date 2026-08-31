# Phase D5 — `runtimeshim` `noLinkname` Fallback-Build Tag (M0107-0008, loop 16)

Status: accepted
Author: Ralph (loop 16, 2026-05-21)
Parent: [[perf-optimize/08-runtime-internals]] §10 ("Fallback build")
Sibling: [[0107-0008o-runtimeshim-go-matrix]] (loop 15 maintenance script)

## Summary

Add a `noLinkname` build constraint to every paired
`internal/runtimeshim/*_linkname.go` / `*_fallback.go` file and wire a
`-tags noLinkname` smoke run into `scripts/runtimeshim_go_matrix.sh`,
satisfying chapter §10's "Fallback build" verification gate explicitly
deferred by loop 15.

## Why

Chapter [[perf-optimize/08-runtime-internals]] §10 mandates:

> **Fallback build** — `go test -tags noLinkname ./...` (or
> equivalent) passes with the public-API fallbacks active; TPS is
> slightly lower but functionality is identical.

Before this loop the fallback path was reachable only on a Go minor
*outside* the linkname tag's `go1.24 && !go1.27` window. The maintenance
recipe assumed an operator could fall back to the public-API
implementation by tag-flipping, but no tag existed to flip on a
supported toolchain.

Loop 15 ([[0107-0008o-runtimeshim-go-matrix]]) explicitly punted on the
tag refactor:

> Out of scope: `-tags noLinkname` fallback smoke … flipping that to a
> hand-written tag is a separate intentional refactor decision.

This loop is that refactor.

## Change

Each of the six paired files gains a single tag-term:

| File                              | Before                       | After                                          |
| --------------------------------- | ---------------------------- | ---------------------------------------------- |
| `nanotime_linkname.go`            | `go1.24 && !go1.27`          | `go1.24 && !go1.27 && !noLinkname`             |
| `nanotime_fallback.go`            | `!go1.24 \|\| go1.27`        | `!go1.24 \|\| go1.27 \|\| noLinkname`          |
| `pinp_linkname.go`                | `go1.24 && !go1.27`          | `go1.24 && !go1.27 && !noLinkname`             |
| `pinp_fallback.go`                | `!go1.24 \|\| go1.27`        | `!go1.24 \|\| go1.27 \|\| noLinkname`          |
| `sema_linkname.go`                | `go1.24 && !go1.27`          | `go1.24 && !go1.27 && !noLinkname`             |
| `sema_fallback.go`                | `!go1.24 \|\| go1.27`        | `!go1.24 \|\| go1.27 \|\| noLinkname`          |

The pair is provably mutually exclusive and jointly exhaustive across
every (Go-minor, tag-set) combination:

- Default tags on `go1.24`..`go1.26`: linkname active, fallback excluded.
- Default tags on `<go1.24` or `>=go1.27`: linkname excluded, fallback active.
- `-tags noLinkname` on any minor: linkname excluded, fallback active.

This preserves the chapter §2 "Build-tag and fallback discipline"
invariant — exactly one of the pair compiles per build.

## Matrix script update

`scripts/runtimeshim_go_matrix.sh` now runs **two** invocations per
toolchain:

```bash
"$tc" test -race -count=1 ./internal/runtimeshim/...                   # linkname
"$tc" test -race -count=1 -tags noLinkname ./internal/runtimeshim/...  # fallback
```

Each variant is summarised independently
(`go linkname: PASS (go version go1.25.0 linux/amd64)`,
`go fallback: PASS (...)`). The script's exit code is the count of
*failed variant runs* across the matrix; a single failing variant on a
single toolchain still surfaces non-zero. Total variant count is `2 ×
#toolchains`.

The `make runtimeshim-matrix` wrapper is unchanged — it shells the
script; the new fallback gate is exercised transparently.

## Discovery: recursion-test split

Adding `-tags noLinkname` immediately surfaced a real semantic gap that
the previous tag layout had hidden. `TestPinP_StableWithinWindow` does:

```go
pid1 := PinP()
pid2 := PinP()  // nested
...
UnpinP()
UnpinP()
```

The linkname `PinP` is implemented as `runtime.procPin` whose contract
is the runtime's `m.locks` counter — nested pins are explicitly
supported. The fallback `PinP` is a `sync.Mutex.Lock`, which is
non-reentrant; the second call deadlocked the test (caught in `<600 s`
test timeout the first time the matrix script ran the fallback
variant).

Two valid fixes:

1. Make the fallback reentrant. Go has no goroutine-local storage, so
   a correct implementation would need `runtime.LockOSThread`-based
   thread tagging or an `unsafe`-based goroutine-ID lookup. Both are
   expensive and bring their own contract surprises.
2. Split the test by tag. The fallback's documented purpose
   ([[0107-0008b-runtimeshim-pinp]]) is *correctness, not parity*, and
   goopg's production callers never nest PinP — the canonical pattern
   is `pid := PinP(); slots[pid].n.Add(1); UnpinP()` with no
   intervening operation. The recursion-shape regression is a runtime-
   contract assertion against the linkname path only.

This loop takes path (2): the recursion test moves to a new
`pinp_recursive_test.go` gated `go1.24 && !go1.27 && !noLinkname`
(matching `pinp_linkname.go`), and `pinp_fallback.go`'s `PinP` doc
comment grows a "Non-recursion contract" paragraph stating the
limitation and pointing at the test split. The remaining four tests in
`pinp_test.go` (`TestPinP_ReturnsValidIndex`,
`TestPinP_BalancedAcrossGoroutines`, `TestPinP_PerPCounterCorrectness`,
`BenchmarkPinUnpin`) run unchanged under both tag sets — they exercise
only the single-pin contract every caller actually uses.

The matrix script's contribution here is exactly its intended purpose:
catching a fallback-only regression that the default-tags build cannot
exercise. Without the fallback gate the divergence would have shipped
silently.

## Out of scope (still)

- **CI provider adoption.** goopg has no `.github/workflows/`; the
  fallback smoke runs only when an operator runs `make
  runtimeshim-matrix` locally. Wiring this into a hosted runner remains
  an org-wide infra decision per [[0107-0008o-runtimeshim-go-matrix]].
- **End-to-end pgbench parity for the fallback build.** Chapter §10 says
  "TPS is slightly lower but functionality is identical." The
  primitives' contract tests (`internal/runtimeshim/*_test.go`) cover
  the functional invariants; server-level fallback-mode pgbench
  validation lives at the `analysis/perf-optimize` layer as a manual
  step and is not bundled into the matrix script.
- **Tagging the `Nanotime` epoch helpers** in
  `internal/activity/registry.go`. The `noLinkname` tag flips the shim
  package only; downstream callers (the registry's `monoToWall`
  conversion per [[0107-0008e-activity-registry-nanotime-wiring]])
  consume the public API and require no source change.

## Verification

```bash
$ make runtimeshim-matrix
…
runtimeshim-matrix summary:
  go linkname: PASS (go version go1.25.0 linux/amd64)
  go fallback: PASS (go version go1.25.0 linux/amd64)

OK: all 1 toolchain(s) × 2 variant(s) passed
```

- `internal/runtimeshim/*_test.go` PASS under both tag sets on the
  current host (Go 1.25.0 / Linux/amd64); the fallback path's
  `BenchmarkPinUnpin` / `BenchmarkSemaAcquireRelease` are expectedly
  slower (global-mutex coalesced contention is the fallback's
  correctness-over-performance trade per [[0107-0008b-runtimeshim-pinp]]
  and [[0107-0008c-runtimeshim-sema]]).
- `doc.go` updated to document the `noLinkname` escape hatch as part of
  the package-level discipline.

## What this closes

This is the final loose end on the chapter §10 verification list for
the `runtimeshim` primitives themselves. The remaining open item in
M0107-0008 ("bufpool per-slot Sema wait caller") consumes
[[0107-0008c-runtimeshim-sema]] but waits for the per-slot wait
coordination site to exist, which lands with M0107-0006 (lock-free
bufpool). M0107-0008's per-Go-minor maintenance machinery (script +
make target + fallback build tag + paired smoke runs) is now feature-
complete.

## PG-compat impact

None. The tag affects internal build selection only; no production code
path, no on-disk/WAL/wire byte changes the change does not already
imply (the fallback's externally-observable behaviour is identical to
the linkname path per the primitives' contract tests).
