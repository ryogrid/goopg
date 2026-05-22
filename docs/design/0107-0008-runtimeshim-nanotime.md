# Design: `internal/runtimeshim` — `Nanotime` (M0107-0008, loop 1)

**Status**: accepted
**Milestone**: M0107-0008 — Phase D5: runtime internals (`//go:linkname` shims)
**Parent design**: [`docs/design/perf-optimize/08-runtime-internals.md`](perf-optimize/08-runtime-internals.md)
**Filed**: 2026-05-21

## Scope of this loop

Introduce the `internal/runtimeshim` package with the first shim:
`Nanotime() int64`. Subsequent loops will add `PinP`/`UnpinP` and
`SemaAcquire`/`SemaRelease`. Call-site wiring (activity registry uses
of `Nanotime`) is deliberately deferred to a follow-up loop so the
package can land standalone with its own race-clean test suite.

## What landed

| File | Build tag | Purpose |
| --- | --- | --- |
| `internal/runtimeshim/doc.go` | (none) | Package-level discipline statement. |
| `internal/runtimeshim/nanotime_linkname.go` | `go1.24 && !go1.27` | `//go:linkname` shim that binds `nanotimeRuntime → runtime.nanotime`. Exposes `Nanotime() int64`. |
| `internal/runtimeshim/nanotime_fallback.go` | `!go1.24 || go1.27` | `time.Now().UnixNano()` fallback. Same public signature. |
| `internal/runtimeshim/nanotime_test.go` | (none) | Monotonicity (`1 << 16` reads), wall-elapsed sanity (50 ms `time.Sleep` ∈ `[25 ms, 500 ms]`), non-zero smoke, and a `BenchmarkNanotime`. |

## Why this shape

`docs/design/perf-optimize/08-runtime-internals.md` §2 spells out the
package-level discipline; this loop instantiates it for `Nanotime`:

- **One package, two files per shim** — keeps the build-tag pairing
  obvious in directory listings. A reviewer can confirm that exactly
  one `nanotime_*.go` is selected on any supported Go minor by
  reading the tag pair.
- **Tag window `go1.24 && !go1.27`** — closes the lower bound at the
  minimum-tested Go minor and the upper bound at the first untested
  major. Bumping the upper bound is the explicit "we ran the
  per-Go-minor smoke matrix" gesture and must accompany the next
  Go-runtime symbol-shape sanity check.
- **Linkname'd name is unexported** (`nanotimeRuntime`) and the public
  API is the wrapper `Nanotime`. Callers never name `runtime.nanotime`
  directly; the wrapper is the only thing the rest of the codebase
  imports. A future linkname-policy hardening in Go therefore touches
  exactly this file, never any caller.

## What we measured

On Linux/amd64, Go 1.25 (this machine, `AMD Ryzen 7 5700X`):

```
$ go test -bench=BenchmarkNanotime -benchtime=200ms -run=^$ ./internal/runtimeshim/...
BenchmarkNanotime-16    12245396         20.54 ns/op
```

That is the goal of the shim — the call cost is dominated by the
linkname'd function-call ABI plus the kernel/VDSO monotonic read.
Practice doc `go_rdbms_performance_techniques.md` §4 cites the
underlying instruction at ~5 ns; the rest is Go function-call
overhead. The numbers we care about for the larger refactor are the
deltas at the call sites in `activity.WaitEventStart` /
`WaitEventEnd`, which still use `time.Now()` today and will switch in
a later loop once the wiring is reviewed in isolation.

For reference, `time.Now()` on the same hardware benchmarks at
roughly 45–55 ns/call (VDSO + wall-clock conversion).

## Test coverage rationale

The package is small enough that every code path is exercised by
unit tests, and `Nanotime` itself has only two behavioural
contracts: monotonicity within the process lifetime and a value
distinct from zero. Both are asserted directly. The
`TestNanotime_ApproximatesWallElapsed` test catches the worst-case
regression where a future refactor accidentally points `Nanotime` at
a constant or at the wall clock instead of the monotonic source — a
50 ms `time.Sleep` would not measure between 25 ms and 500 ms in
that misconfiguration. The bounds are generous on purpose so the
test does not flake on contended CI runners.

## PG-compat impact

None. This is a Go-runtime accessor; no on-disk format, wire
protocol, or catalog touch.

## Follow-ups (separate loops)

- `internal/runtimeshim/pinp_*.go` — `PinP() int` /
  `UnpinP()` for per-P xid cache and per-P stats counters.
- `internal/runtimeshim/sema_*.go` — `SemaAcquire`/`SemaRelease`
  via `sync.runtime_Semacquire` / `runtime_Semrelease` for the
  per-slot bufpool I/O-inflight wait that
  [`06-bufpool-lockfree`](perf-optimize/06-bufpool-lockfree.md) §4
  needs.
- Activity-registry rewrite to call `runtimeshim.Nanotime()` from
  `WaitEventStart`/`WaitEventEnd`, picking up the ~30 ns/call delta
  at ~30 K calls/sec on the c=100 SO workload (per
  [`05-activity-perbackend`](perf-optimize/05-activity-perbackend.md)).
- Per-Go-minor CI smoke matrix (Go 1.24 / 1.25 / 1.26) on this
  package only, so the runtime-symbol-shape break point lands here
  rather than in the field.
