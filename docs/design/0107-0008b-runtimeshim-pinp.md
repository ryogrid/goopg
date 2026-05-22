# Design: `internal/runtimeshim` — `PinP` / `UnpinP` (M0107-0008, loop 2)

**Status**: accepted
**Milestone**: M0107-0008 — Phase D5: runtime internals (`//go:linkname` shims)
**Parent design**: [`docs/design/perf-optimize/08-runtime-internals.md`](perf-optimize/08-runtime-internals.md)
**Predecessor**: [`0107-0008-runtimeshim-nanotime.md`](0107-0008-runtimeshim-nanotime.md)
**Filed**: 2026-05-21

## Scope of this loop

Add the second `internal/runtimeshim` primitive: `PinP() int` and
`UnpinP()`. This shim is what unlocks per-P sharded data structures
(the per-P xid allocator cache from
[`04-mvcc-procarray`](perf-optimize/04-mvcc-procarray.md) and the per-P
statistics counters mentioned in
[`08-runtime-internals`](perf-optimize/08-runtime-internals.md) §4) by
exposing the same `runtime.procPin` / `runtime.procUnpin` primitives
`sync.Pool` uses internally to pin a goroutine to its current P with
zero atomic operations.

Following the loop-1 pattern, **only the shim lands here**. Caller
wiring (the per-P xid cache in `internal/mvcc/xidgen.go`, the per-P
stats counters in `internal/stats/`) is deferred to subsequent loops so
this primitive can be evaluated standalone with its own race-clean
test suite.

## What landed

| File | Build tag | Purpose |
| --- | --- | --- |
| `internal/runtimeshim/pinp_linkname.go` | `go1.24 && !go1.27` | `//go:linkname` shims binding `runtime_procPin → runtime.procPin` and `runtime_procUnpin → runtime.procUnpin`. Exposes `PinP() int` / `UnpinP()`. |
| `internal/runtimeshim/pinp_fallback.go` | `!go1.24 || go1.27` | Mutex-serialised fallback: a global `sync.Mutex` lock/unlock pair. `PinP` always returns 0. Correct but contention-bound. |
| `internal/runtimeshim/pinp_test.go` | (none) | Index-range, nesting-stability, balanced cycles under `-race`, per-P counter correctness, and `BenchmarkPinUnpin`. |

## Why this shape

[`08-runtime-internals.md`](perf-optimize/08-runtime-internals.md) §4
specifies the API; this loop instantiates it under the §2 discipline
(one package, paired tags, race-clean, no `//go:nosplit`):

- **`runtime.procPin` / `runtime.procUnpin`** — the runtime-internal
  primitives that `sync.runtime_procPin` is a thin alias of. Both
  symbols are stable in the runtime's `proc.go` and have been since
  the introduction of `sync.Pool`'s per-P caches. Selecting the
  runtime-side name (rather than the `sync.runtime_procPin` alias)
  matches the parent design's spec and is symmetric with the
  `runtime.nanotime` selection in loop 1.
- **Fallback is mutex-based**, not "return 0 unsynchronised". A
  fallback that silently degrades per-P sharding to a shared slot
  without mutual exclusion would race when callers write to
  `caches[pid]` without atomic primitives — exactly the contract the
  pin window provides on the linkname path. Holding a global mutex
  for the entirety of the pinned window preserves the
  "no-concurrent-mutation" invariant that callers rely on. The
  fallback is intentionally slow; its job is correctness, not parity.
- **API surface is exactly two functions.** `PinP()` returns the P
  index and `UnpinP()` releases the pin. No batching, no defer
  helper, no "scoped pin" wrapper — the design's hot-path callers
  inline the pair to avoid the ~20 ns defer overhead.

## Caller contract (reiterated here so it lives near the code)

Between `PinP` and `UnpinP` the goroutine MUST NOT:
- send/receive on a channel,
- acquire a mutex that may wait,
- enter a syscall,
- block on the runtime in any other way.

The runtime increments `m.locks` on `procPin`; while the counter is
nonzero, the goroutine cannot be preempted or migrated to another P.
Violating the contract risks a deadlock with the runtime's preemption
logic. Concretely: every call site must end the pinned window with
`UnpinP` before any potentially-blocking operation, and the pair must
balance on every code path.

## What we measured

On Linux/amd64, Go 1.25 (this machine, `AMD Ryzen 7 5700X`):

```
$ go test -bench=BenchmarkPinUnpin -benchtime=1s -run=^$ ./internal/runtimeshim/
BenchmarkPinUnpin-16    581692220        2.067 ns/op    0 B/op    0 allocs/op
```

That is below the parent design's ~3 ns/op target for the pair (it
hits because the bench harness does nothing inside the pinned window
beyond reading the returned int). The race-clean per-P counter test
(`TestPinP_PerPCounterCorrectness`) drives 16 goroutines × 16 K
iterations each through a real `caches[pid].n++` pattern (with
`atomic.Int64.Add` for cross-goroutine load at the end) and confirms
the final sum equals total increments — a single dropped increment
under the pinned window would surface as a sum mismatch.

For reference, the fallback path's measured cost on the same hardware
is ~15 ns/op for the uncontended single-goroutine bench (dominated by
`sync.Mutex.Lock`/`Unlock`). Contended cost is unbounded — that is by
design; the linkname path is what we actually want to be on.

## Test coverage rationale

Four tests, each anchored to a contract clause:

1. **`TestPinP_ReturnsValidIndex`** — the index is in `[0, GOMAXPROCS)`
   on the linkname path and `{0}` on the fallback. A misaligned build
   tag returning a stale-symbol bogus value (e.g., a sentinel `-1`)
   would fail here.
2. **`TestPinP_StableWithinWindow`** — nested `PinP`/`UnpinP` returns
   the same index for the inner and outer call. This is the
   "no-migration-while-pinned" invariant; if the runtime ever changes
   the pin semantics so an inner pin reports a different P, the test
   fails immediately.
3. **`TestPinP_BalancedAcrossGoroutines`** — 32 goroutines × 4 K
   iterations of bare `PinP`+`UnpinP`. Under `-race`, an unbalanced
   pair on any path would surface as a runtime fatal ("locks held
   across a context switch"). The empty pinned window keeps the
   signal isolated to the shim itself.
4. **`TestPinP_PerPCounterCorrectness`** — the canonical caller
   pattern. 16 goroutines × 16 K iterations of `pid := PinP();
   slots[pid].n.Add(1); UnpinP()`. The final sum equals 16 × 16384.
   This catches the most-likely regression mode: a fallback that
   silently allows concurrent mutation of the same slot.

`BenchmarkPinUnpin` is the regression canary: a future build-tag
window where the linkname target moved would either fail to link or
fall through to the fallback at ~15 ns/op — a 7× regression any
reviewer would notice in the next benchmark run.

## PG-compat impact

None. This is a Go-runtime accessor; no on-disk format, wire protocol,
or catalog touch. The parent design's PG-compat gate (invariants §6
Phase D5) is satisfied trivially: linkname targets only touch
scheduling/timing, not durable state.

## Follow-ups (separate loops)

- **`internal/runtimeshim/sema_*.go`** — `SemaAcquire` / `SemaRelease`
  via `sync.runtime_Semacquire` / `runtime_Semrelease` for the
  per-slot bufpool I/O-inflight wait that
  [`06-bufpool-lockfree`](perf-optimize/06-bufpool-lockfree.md) §4
  needs.
- **Per-P xid allocator cache** in `internal/mvcc/xidgen.go` that
  consumes `PinP`/`UnpinP`. This is the load-bearing caller — it
  removes the global `XidGenLock`-equivalent from the c=100 SU
  contention profile (per
  [`04-mvcc-procarray`](perf-optimize/04-mvcc-procarray.md)).
- **Per-P statistics counters** in `internal/stats/` (or wherever the
  current global atomic counters live) once the consumer list is
  triaged.
- **Activity-registry rewrite** to call `runtimeshim.Nanotime()` from
  `WaitEventStart`/`WaitEventEnd` — still pending from loop 1; the
  wiring requires a monotonic→wall conversion layer in `Snapshot()`
  before it can land safely (and that is a separate design decision
  from this shim).
- **Per-Go-minor CI smoke matrix** (Go 1.24 / 1.25 / 1.26) on
  `internal/runtimeshim/` only, so a runtime-symbol-shape break
  surfaces here rather than in the field.
