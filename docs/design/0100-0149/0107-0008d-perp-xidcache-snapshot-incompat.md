# 0107-0008d — Per-P XID Cache Is Incompatible with Current Snapshot Semantics

**Status:** FINDING. The per-P XID allocator cache specified in
[perf-optimize/08-runtime-internals.md §4 "Use case 1"](../../perf-optimize/08-runtime-internals.md)
cannot be wired into `internal/mvcc/XidGen` without a parallel rewrite of
`captureSnapshot` and the visibility model. This document records the
incompatibility so a future loop does not re-attempt the same wiring.

Parent: [[0107-0008-runtimeshim-nanotime]],
[[0107-0008b-runtimeshim-pinp]],
[[0107-0008c-runtimeshim-sema]]
(the three shim primitives are race-clean and stand on their own; only
the *XID cache caller* is blocked by this finding).

## The wiring that was attempted (and rolled back)

`internal/mvcc/xidgen.go` was rewritten to add a `caches [256]perPXidCache`
field, each `perPXidCache` packing `(end<<32 | next)` into one
`atomic.Uint64`. `Allocate()` pinned its P via `runtimeshim.PinP()`,
served xids from the local cache, and refilled with a single
`g.next.Add(xidCacheBatch)` (batch = 32) on cache miss.

The change passed every test in `internal/mvcc/` and `internal/runtimeshim/`
including a 32-goroutine × 4 K-allocation uniqueness stress, but
deterministically broke
`internal/server.TestUpsertDoNothing_WaitsForInFlightDelete` (which holds
on every other branch). The roll-back is in the same loop.

## Root cause

The current snapshot model in `Manager.captureSnapshot` (`internal/mvcc/manager.go:600`)
relies on two invariants the cached allocator silently violates:

1. **Monotonic xid assignment across backends.** With a single
   `atomic.Uint64` counter, the *kth* `Allocate()` call (by wall-clock
   order across all goroutines) returns the *kth* xid. A later
   Allocate cannot return a smaller value than an earlier one.
   Per-P caching breaks this: P_a's cache window `[3, 35)` and P_b's
   cache window `[35, 67)` are interleaved temporally as their
   goroutines schedule, so the sequence of returned xids can be
   `3, 35, 4, 36, 37, 5, …`. A backend that allocates xid 4 *after*
   another backend has been running with xid 35 produces a heap tuple
   whose `xmin = 4` was assigned **later** than another whose
   `xmin = 35`.

2. **Snapshot.Xmax accounts for every xid that has been assigned.**
   `captureSnapshot` sets `xmax = xidgen.Peek()` and then walks
   `procArray` collecting xids `< xmax` into `InProgress`. The
   visibility model treats `xmin >= xmax` as "future, invisible" and
   relies on the implication "if `xmin < xmax` then either the txn is
   in `InProgress`, or its outcome is recorded in CLOG". With per-P
   caching, neither possible definition of `Peek` preserves this
   implication:

   - **`Peek = min(cache.next ∀ active P, global)`** (the variant
     that prevents future cached xids from being mis-classified as
     "past"): then **currently-issued** xids on other Ps may exceed
     `xmax` and be filtered out by the `xid >= xmax` check inside the
     procArray walk, so they never make it into `InProgress`. A
     reader using this snapshot mis-classifies live, in-flight
     transactions as "future" and treats their heap tuples as
     invisible. This is the regression that fired in the upsert wait
     test: setup's `xmin = 35` tuple was filtered out of the
     subsequent backend's snapshot, the DELETE matched no rows, the
     ON CONFLICT DO NOTHING saw no conflict, and the final table
     held both `(1,'old')` and `(1,'new')`.
   - **`Peek = global.Load()`** (the variant that keeps all
     currently-issued xids `< xmax`): then xids that are
     **cached-but-not-yet-issued** at snapshot time can be handed out
     to a *new* transaction afterwards. That new txn's xid satisfies
     `xid < snapshot.Xmax`, is **not** in `snapshot.InProgress`
     (which was frozen at snapshot time), and is not in
     `aborted`. When the new txn commits, the snapshot reader's
     visibility check finds CLOG-committed and concludes "committed
     before snapshot" — a phantom read of a transaction that started
     **after** the snapshot.

   The two variants exchange one set of broken invariants for
   another. There is no choice of `Peek` that satisfies both with the
   current visibility model.

## Why the design doc didn't catch this

`perf-optimize/08-runtime-internals.md §4` argues correctness solely
from the leakage angle ("cached-but-unused xids are never recorded in
any procSlot and never written to CLOG, so they are invisible by
default"). That argument covers xids that are *never issued*. It does
not address the case where a cached xid **is** issued later, which is
the normal hot-path case and the one the visibility model relies on
behaving as if every issued xid is `< current Peek()`.

The PG analogue in the same section ("PG bulk-allocates xids inside
`XidGenLock`") is also inaccurate: `GetNewTransactionId` in
`postgres/src/backend/access/transam/varsup.c` allocates one xid per
call under the lock. PG does not avoid the per-allocation atomic; it
*centralises* it. That centralisation is precisely what makes
`Snapshot::xmax = ShmemVariableCache->nextXid` an exact upper bound
on every assigned xid.

## What this means for M0107-0008

The three `runtimeshim` primitives are independently usable:

- `runtimeshim.Nanotime` — wiring into `internal/activity/registry.go`
  to replace `time.Now().UnixNano()` calls on `WaitEventStart` /
  `WaitEventEnd`. No snapshot interaction. Requires a one-off
  monotonic→wall conversion in `Snapshot()` for `pg_stat_activity`
  display (the design doc §3 sketches it).
- `runtimeshim.PinP` / `UnpinP` — wiring into per-P **statistics
  counters** (design doc §4 "Use case 2"). These counters are
  best-effort aggregates over `Sum()`; no ordering or transactional
  semantics. Safe under the same pinning discipline that this loop
  proved works for the xid cache.
- `runtimeshim.SemaAcquire` / `SemaRelease` — wiring into
  `internal/storage` bufpool per-slot I/O-inflight waits (design doc
  §5; chapter [[06-bufpool-lockfree]]). Replaces a per-partition
  `sync.Cond`; no snapshot interaction.

**The XID cache caller is removed from M0107-0008's scope.** Bringing
it back requires either:

(a) a new `captureSnapshot` algorithm that does not key on a single
xmax value (e.g., snapshot stored as the set of currently-running xids
plus per-P watermarks of issued ranges), or

(b) a different optimization for the global counter that keeps xids
monotonically assigned globally (e.g., `XADD`-with-tighter-batch on a
single counter and accept the global atomic in the hot path —
measurements at the c=100 SU target (~7K xacts/sec × 1 xid/txn) suggest
a single uncontended atomic add is below the noise floor of the
profile that motivates the cache).

Either change is materially larger than the per-loop budget of
M0107-0008. Both should be filed as separate sub-milestones if
performance measurements after the other Phase D work justify them.

## Verification

The shim-only progress from loops 1-3 is unaffected by this finding:

```
go test -race -count=1 ./internal/runtimeshim/     # PASS
go test -race -count=1 ./internal/mvcc/             # PASS
go test -race -count=1 ./internal/server/           # PASS (TestUpsertDoNothing_WaitsForInFlightDelete restored)
go test -race -count=1 ./internal/executor/         # PASS
go test -race -count=1 ./internal/wal/              # PASS
go test -race -count=1 ./internal/storage/          # PASS
```

The rolled-back implementation lives in the loop-4 git history and is
not in the working tree.
