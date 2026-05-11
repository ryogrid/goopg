# Design 0090-0002 — UPDATE concurrent-xmax overwrite fix

**Status:** authoritative for M0090-0002 implementation.
**Milestone:** [M0090](../milestones/0090-pgbench-scale-100-mvcc-and-insert-bugs.md).

## Problem

After a 180s pgbench standard run at scale 100 (-c 100 -j 100),
`SELECT count(*) FROM pgbench_branches` returns 1,610 visible
rows instead of 100. The workload only UPDATEs these rows — no
INSERTs — yet the visible count drifts upward over time.

Root cause: `tryApplyHOTUpdate`
(`internal/executor/operators_storage.go:547-628`) does not
detect when another transaction has already stamped xmax /
HeapHotUpdated on the old tuple, and silently overwrites the
prior stamp. The orphan new tuple from the prior transaction
remains visible under MVCC.

### Detailed race

T1 (xid=1001) and T2 (xid=1002) both UPDATE the same row
identified by an index lookup:

1. T1 reads slot S via `followHOTChain` (RLock), queues
   `pending{slot=S, newRow=T1's}`. RLock released.
2. T2 reads slot S via `followHOTChain` (RLock), queues
   `pending{slot=S, newRow=T2's}`. RLock released.
3. T1 calls `tryApplyHOTUpdate(blk, oldSlot=S, T1's_row)`.
   Exclusive Lock acquired. Pre-check: slot S is LP_NORMAL ✓.
   `PageAddHeapTuple` writes T1's tuple at slot S'.
   `PageStampHotOldTuple(page, S, T1.xid, blk, S')` stamps:
   `S.tuple.xmax = T1.xid`, `S.tuple.infomask |=
   HeapHotUpdated`, `S.tuple.CTID = (blk, S')`.
   Lock released, T1 commits.
4. T2 calls `tryApplyHOTUpdate(blk, oldSlot=S, T2's_row)`.
   Exclusive Lock acquired. Pre-check: slot S is **still
   LP_NORMAL** (HOT-update changes the tuple header, not the
   line-pointer flags).
5. `PageAddHeapTuple` writes T2's tuple at slot S''.
6. `PageStampHotOldTuple(page, S, T2.xid, blk, S'')` overwrites:
   `S.tuple.xmax = T2.xid` (clobbers T1.xid!),
   `S.tuple.CTID = (blk, S'')`.
7. Lock released, T2 commits.

End state on the page:
- S (original): xmax = T2, CTID = (blk, S''), HeapHotUpdated set.
- S' (T1's new tuple): xmin = T1 committed, xmax = invalid →
  visible.
- S'' (T2's new tuple): xmin = T2 committed, xmax = invalid →
  visible.

A reader's snapshot that includes both T1 and T2 as committed
sees S', S'' as visible rows. The HOT chain from S follows CTID
to S'' only; S' is orphaned but still on the page and still
passes MVCC visibility (its xmin is committed, its xmax is 0).

Over 12 000 transactions × 5 UPDATEs each at -c 100 against
100 branches rows, orphans accumulate. End state: ~1 610
visible rows from 100 logical rows.

### Why M0088/M0089's race-tolerance commits don't help

Commits 18c60d9 (the 3 updateOp/deleteOp xmax sites) and 2c1e18e
(the HOT-path post-prune check) tolerate the
LP_NORMAL → LP_DEAD/LP_UNUSED transition by `continue`ing past
`ErrUnsupportedItem`. They do NOT detect the
LP_NORMAL-with-existing-xmax case because
`PageSetHeapTupleXmax` / `PageStampHotOldTuple` happily
overwrite xmax without checking whether it was already set.

## Approach

Detect concurrent xmax stamps under the page's exclusive Lock,
and fail the transaction with SQLSTATE `40001`
(serialization_failure) when detected. The Lock guarantees no
further mutations can race the check.

PG's READ_COMMITTED path uses EvalPlanQual to re-fetch + re-
evaluate the predicate against the latest tuple version. goopg
does not have EvalPlanQual; the safe alternative is to fail.
This trades throughput for correctness — under heavy contention
on the same row, transactions abort and clients must retry. For
pgbench, this manifests as a non-zero
"serialization_failure" abort count; for production, retries
are the application's responsibility.

### Implementation

Add a helper:

```go
// isConcurrentlyUpdated reports whether the tuple has been
// updated/deleted by ANY transaction other than ourselves
// (xmax != invalid, OR HeapHotUpdated bit set). Called under
// the page's exclusive Lock to detect a concurrent update
// between scan-time tuple-fetch and modify-time xmax stamp.
func isConcurrentlyUpdated(h storage.HeapTupleHeader, myXID storage.TransactionID) bool {
    if h.Infomask&storage.HeapHotUpdated != 0 {
        return true
    }
    if h.Xmax != storage.InvalidTransactionID && h.Xmax != myXID {
        return true
    }
    return false
}
```

(`h.Xmax == myXID` means our own re-update of the same row in
the same transaction, which is legal.)

Wire this check at 4 sites:

1. **`tryApplyHOTUpdate`** (line ~580, after the existing pre-
   check on slot flags):
   - `PageGetHeapTuple(page, oldSlot)` → check
     `isConcurrentlyUpdated(tuple.Header, ctx.Tx.XID)`.
   - If true: release Lock + Unpin, return a typed
     `serialization_failure` error.

2. **`updateOp.updateViaIndex` non-HOT branch** (line ~776,
   before `PageSetHeapTupleXmax`):
   - Same `PageGetHeapTuple` + `isConcurrentlyUpdated` check.

3. **`updateOp.Next` non-HOT branch** (line ~882): same.

4. **`deleteOp.Next`** (line ~978): same.

Each site returns the serialization error via:

```go
return ptr, &ExecError{
    Code: "40001",
    Pos: o.plan.Pos(),
    Message: "could not serialize access due to concurrent update",
}
```

(`tryApplyHOTUpdate` returns `(false, err)` — the caller treats
the err as a transaction-abort signal, same as any other write
error.)

### Why the check is race-free

The check happens under the page's exclusive Lock. Under that
Lock:
- No other writer can call `PageAddHeapTuple`,
  `PageSetHeapTupleXmax`, or `PageStampHotOldTuple` on this
  page (they all require the Lock).
- Readers using RLock can't mutate.

So reading the tuple header + acting on its xmax is atomic
with respect to other writers. If another transaction stamped
xmax before we got the Lock, we see it; if no one has, we
proceed with our stamp.

### The race-tolerance commits (18c60d9, 2c1e18e) interaction

Those commits handle the LP_NORMAL → LP_DEAD/LP_UNUSED
transition by skipping the row (`continue`). They are correct
for that case — the row no longer exists, so there's nothing
to stamp.

The new M0090-0002 check handles the LP_NORMAL-with-existing-
xmax case. The two checks are complementary:
- LP transitioned out of NORMAL → skip (M0088).
- LP still NORMAL but xmax already set → fail (M0090-0002).
- LP still NORMAL and xmax unset → proceed (happy path).

## Throughput impact

Under high concurrent UPDATE contention (-c 100 against 100
rows), the strict-fail policy will cause many transactions to
abort. The exact rate depends on the workload pattern; pgbench
at scale 100 should see a meaningful but bounded abort rate.

A future milestone (M0091, filed when the abort rate is
measured) will implement EvalPlanQual / lock-and-re-fetch to
restore PG-equivalent concurrency without sacrificing
correctness.

## Test coverage

`internal/executor/concurrent_update_xmax_test.go` (NEW):

1. **TestConcurrentHOTUpdateDetectsRace** — two goroutines
   issue `UPDATE T SET x = x + 1 WHERE id = 1` concurrently.
   One transaction succeeds (commits); the other returns the
   `40001` serialization error. Pre-fix both succeed; post-
   fix exactly one succeeds.

2. **TestConcurrentHOTUpdateNoDuplicateVisibleRow** — after the
   race resolves, `SELECT count(*) FROM T WHERE id = 1` returns
   1, not 2. Pre-fix this returns 2 (the bug); post-fix returns
   1 (the failed transaction was aborted; only the winner's
   new tuple is visible).

3. **TestSelfUpdateInSameTransactionNotBlocked** — within a
   single transaction, `UPDATE T SET x = 1 WHERE id = 1`
   followed by `UPDATE T SET x = 2 WHERE id = 1` succeeds (the
   `h.Xmax == myXID` exclusion preserves single-txn re-update
   semantics).

## Acceptance

- Unit tests pass.
- pgbench scale-100 standard workload: `branches` row count
  stays at 100, `tellers` at 1 000.
- pgbench simple-update post-restart auto-detects scale=100
  correctly and runs without `short read at block`.
