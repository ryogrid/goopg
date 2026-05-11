# Design 0092-0001 — indexScanOp lazy iteration

**Status:** authoritative for M0092-0001 implementation.
**Milestone:** [M0092](../milestones/0092-lazy-row-emission-in-scan-and-project.md).

## Problem

Post-M0091 pprof shows `executor.cloneRow → acquireRow →
rowPool.Get → New → make(Row, width)` as 34 % of allocs
(~88 KB / query) in the pgbench select-only workload. The
load-bearing path is `indexScanOp`'s scanFn at
`internal/executor/operators_index.go:285`:

```go
o.rows = append(o.rows, cloneRow(row))
```

Per match, `cloneRow` calls `acquireRow(width)` which hits
the rowPool's `New` because cloned Rows are NEVER returned
(consumers retain `TupleSlot` references past `Close()`).
The pool is effectively cold; every cloneRow allocates fresh.

## Approach: TID-list-eager + heap-fetch-lazy

Replace the eager Row-materialisation pattern with two
phases:

1. **Open / Rescan** — `tree.RangeScan(lo, hi, scanFn)`
   collects ONLY `storage.ItemPointer`s into `o.tids`. No
   HOT-chain follow, no DecodeRow, no cloneRow.
2. **Next()** — for the i-th TID, Pin the heap block, RLock,
   `followHOTChain`, decode into the reusable `o.scanRow`,
   detoast if needed, RUnlock + Unpin, return
   `asSlot(o.Schema(), o.scanRow)`.

The slot returned by Next() aliases `o.scanRow`. The next
Next() overwrites `o.scanRow` with a different tuple's data.
**Contract: callers must consume / materialize the slot
before the next Next() call.**

Per the M0091 + M0092 audits, this contract is already
honored by every consumer:
- pass-through ops (`filterOp`, `limitOp`) forward the slot;
- retain-and-stop ops (`sortOp`, `windowOp`, `lockRowsOp`)
  call `slot.Materialize()` before retention;
- `joinOp` copies via `drainRowsCtx`;
- `aggregateOp` extracts Datums per row + `MaterializeArena`;
- `nestedLoopIndexJoinOp` retains outerRow — fixed in
  Commit B by deep-copying into `o.currentOuter`;
- the simple-query / extended-query result loops fully
  format cells into a fresh `[][]byte` per iteration.

## Drop the M0073 arena from indexScanOp

The arena's batching benefit (amortise allocations across
multiple rows decoded in one batch) does NOT apply when only
one row is in flight per Next() call. Specifically:

- After we Unpin the heap page, we cannot retain page-aliased
  bytes. The arena pages would have to be reset (losing the
  data) or kept (no batching benefit since the next decode
  writes the same arena pages).
- Per-Next allocations for variable-length columns fall back
  to `make([]byte)` (the pre-M0073 path via
  `DecodeRowInto(nil arena)`).

Net allocation profile: same as pre-M0091 for the variable-
length-column case (since cloneRow's allocation is replaced
by the per-column make). For pgbench (int columns only), zero
arena allocations.

## Concurrency / locking semantics

Per-Next locking pattern:
1. `Pool.Pin(BufferTag{Rel: heapRel, Block: tid.Block})`
2. `slot.RLock()`
3. `followHOTChain(slot.Page(), tid.Offset, snap, txID)`
4. `slot.RUnlock()`
5. `Pool.Unpin(slot)`
6. `DecodeRowInto(scanRow, cols, tuple.Data)`

Step 6 happens AFTER RUnlock + Unpin, but `tuple.Data` is a
`[]byte` returned by `followHOTChain`. This is a copy of the
tuple bytes (parsed during step 3 under the RLock), not an
alias to the page bytes — verified by reading
`storage.PageGetHeapTuple` and `storage.ParseHeapTuple` which
both copy the tuple bytes into a fresh `[]byte` during parse.

So decode-after-unpin is safe.

## `lockedByForeign` retry path

The current code (`operators_index.go:711-734`) detects
foreign-tuple-lock state inside scanFn, acquires a
`lockmgr.ExclusiveLock` on the tuple, then re-follows the HOT
chain. In the lazy refactor this logic moves into Next():

```go
tuple, actualSlot, found := followHOTChain(...)
if !found { return o.Next() } // skip invisible, recurse
if foreignLockOnly(tuple.Header, ctx.Tx.XID) {
    ptr := storage.ItemPointer{Block: tid.Block, Offset: actualSlot}
    if err := ctx.acquireTupleLock(rel, ptr, lockmgr.ExclusiveLock); err != nil {
        return nil, err
    }
    // re-follow HOT under fresh pin
    slot2, _ := ctx.Pool.Pin(...)
    slot2.RLock()
    tuple, _, found = followHOTChain(slot2.Page(), tid.Offset, snap, xid)
    slot2.RUnlock()
    ctx.Pool.Unpin(slot2)
    if !found { return o.Next() }
}
```

Same lock-acquisition order as the current code; just moved
from per-scanFn invocation to per-Next.

## Test coverage

- Existing `internal/executor/operators_index_test.go`
  tests + integration tests must continue to pass.
- Existing TPC-H + pgbench correctness tests must continue
  to pass.
- New benchmark `BenchmarkIndexScanPointLookup` exercising
  Open + Next on a 10K-row pkey for unit-level
  before/after comparison.

## Expected impact

Per-Next allocation breakdown (rough):
- `acquireRow(width)` — once at Open; reused across Next.
- HOT-chain follow returns a tuple with `[]byte Data` —
  one allocation in `ParseHeapTuple`.
- `DecodeRowInto` — per-column `make([]byte)` for variable-
  length columns. For pgbench (int columns only), zero.
- `asSlot` — currently allocates a `MaterializedSlot`
  struct; out of M0092 scope (filed as M0093 candidate if
  material).

For pgbench's `SELECT abalance FROM pgbench_accounts WHERE
aid = :aid`, per-Next allocations should drop from ~88 KB to
under 1 KB (just the tuple ParseHeapTuple copy + the slot
struct).
