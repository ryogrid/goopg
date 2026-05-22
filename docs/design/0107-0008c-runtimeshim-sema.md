# 0107-0008c — `runtimeshim.SemaAcquire` / `SemaRelease`

Loop 3 of M0107-0008 (Phase D5: runtime internals). Companion to
[[0107-0008-runtimeshim-nanotime]] and [[0107-0008b-runtimeshim-pinp]];
parent chapter is
[[docs/design/perf-optimize/08-runtime-internals]] §5.

## 1. Scope

Adds the third and final `//go:linkname` primitive specified by §5 of
the parent chapter:

- `SemaAcquire(s *uint32)` — block until `*s > 0`, then atomically
  decrement.
- `SemaRelease(s *uint32)` — atomically increment `*s` and wake one
  waiter (if any) parked on the same address.

Caller wiring is **not** part of this loop. The per-slot I/O-inflight
wait described by [[06-bufpool-lockfree]] is the canonical consumer
and will land separately; this loop deliberately confines the change
to the shim package so the contract-anchored test suite can be
validated standalone.

## 2. Linkname targets

| Symbol            | Linkname target              | Signature                                                   |
|-------------------|------------------------------|-------------------------------------------------------------|
| `runtime_Semacquire` | `sync.runtime_Semacquire` | `func runtime_Semacquire(s *uint32)`                        |
| `runtime_Semrelease` | `sync.runtime_Semrelease` | `func runtime_Semrelease(s *uint32, handoff bool, skipframes int)` |

The shim deliberately targets the `sync`-package-internal aliases
rather than the runtime-internal `runtime.semacquire` /
`runtime.semrelease` names: the `sync.runtime_*` symbols are the
de-facto stable external API the standard library itself depends on
(`sync.Mutex`, `sync.WaitGroup`, `sync.Cond`, `sync.Once`, …) and have
tracked the runtime's internal renames across Go versions without
breaking these callers. Parent chapter §5 spells this out.

`SemaRelease` calls the underlying primitive with `handoff=false,
skipframes=0`. Non-handoff release matches `sync.Mutex.Unlock`'s call
site and is the right default for the bufpool's "I/O finished; any
waiter may now proceed" pattern, where every parked Pin caller is
equally eligible to take ownership. Handoff mode would force the
released unit to a specific waiter, which is the wrong semantics for
buffer-slot wakeups and adds overhead besides.

## 3. Fallback

`sema_fallback.go` carries the inverse build tag and uses a global
`sync.Mutex` plus a lazily-populated `map[*uint32]*sync.Cond`. Every
fallback semaphore in the process serialises on the same mutex —
correctness is preserved (the canonical "block while zero, decrement
on positive, signal on release" sequence) but throughput collapses
proportional to total cross-cell contention.

The map grows monotonically: `*uint32` cells are not removed when
their last waiter departs. This mirrors the linkname path's
externally-observable contract — the runtime's address-keyed wait
list has no destruction hook either, and producing one in the
fallback would diverge the two paths' observable behaviour.

## 4. Pin/Sema relationship

`SemaAcquire` may park the calling goroutine. It is therefore **not**
safe to call inside a PinP/UnpinP window: a parked goroutine inside a
pinned window stalls the runtime's preemption logic, which can
deadlock other goroutines pinned to the same P (and breaks the
`m.locks > 0` invariant the runtime relies on). The two primitives
are complementary, not nestable. The fallback exhibits the same
hazard: the global pin mutex and the global sema mutex are distinct,
and acquiring the latter inside a pinned window provides no benefit
while extending the critical section.

This constraint is documented at the call site (`sema_linkname.go`)
and is the reason the bufpool's design queues sema waits at the
fast-path's exit point rather than inside a slot's pinned scope.

## 5. Tests

`sema_test.go` carries four contract-anchored tests + one micro-bench:

| Test                                  | Contract clause exercised                                                                                                              |
|---------------------------------------|----------------------------------------------------------------------------------------------------------------------------------------|
| `TestSema_PreReleasedAcquireReturns`  | Acquire on a positive cell decrements and returns without blocking; pre-issued Releases stack rather than coalesce.                    |
| `TestSema_BlocksUntilRelease`         | Acquire on a zero cell parks the goroutine; a subsequent Release on the same cell wakes one waiter.                                    |
| `TestSema_BalancedManyProducersConsumers` | 8 producers × 4096 Releases × 8 consumers × (totalOps/8) Acquires: every Acquire pairs with exactly one Release; final `*s == 0`. |
| `TestSema_DistinctCellsIndependent`   | Releases on cell B do not wake Acquires parked on cell A; per-cell wait queues are address-keyed.                                       |
| `BenchmarkSemaAcquireRelease`         | Uncontended pair cost. Linkname path on Linux/amd64 Go 1.25: **5.6 ns/op** (cell stays positive; no goroutine park).                  |

All four tests PASS under `go test -race -count=1
./internal/runtimeshim/` (1.22 s).

## 6. Build-tag posture

Both files carry the same window the existing primitives use:

```
//go:build go1.24 && !go1.27    // sema_linkname.go
//go:build !go1.24 || go1.27    // sema_fallback.go
```

This loop verifies the shim on Go 1.25 (current toolchain on the dev
machine). Promoting the upper bound is the explicit per-Go-minor CI
matrix item tracked under M0107-0008's outstanding work.

## 7. Out of scope for this loop

- **Bufpool wiring.** [[06-bufpool-lockfree]] specifies the per-slot
  wait protocol; integrating `SemaAcquire`/`SemaRelease` into the
  Pin slow path lands with its own design doc.
- **Per-Go-minor CI matrix.** Calls for separate CI plumbing under
  M0107-0008; no code change in `internal/runtimeshim/`.
- **Handoff-mode variant.** Not adopted; no current call site needs
  the strict handoff semantics, and adding it preemptively would
  bloat the shim's contract.

## 8. Verification

- `go test -race -count=1 ./internal/runtimeshim/` — PASS (1.22 s).
- `BenchmarkSemaAcquireRelease` — 5.6 ns/op on Linux/amd64 Go 1.25.
- `make ralph-state-guard` — gated at loop close.

The shim's surface is intentionally narrow: two exported functions,
two unexported linkname targets, one fallback file. Future
caller-wiring loops can rely on this surface without re-verifying the
linkname binding.
