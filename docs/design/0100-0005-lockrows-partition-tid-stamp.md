# M0100-0005 follow-up: lockRowsOp per-row xmax stamp on partitioned tables

## Problem

`SELECT … FOR UPDATE / FOR SHARE` on a partitioned table was not stamping the
lock-only xmax on the heap tuple in the leaf partition.

`lockRowsOp.drainAndStamp` relies on `findScanLeaf` to locate a
`currentTIDProvider` (either `seqScanOp` or `indexScanOp`) in the child
operator chain. For a non-partitioned table the chain is:

```
lockRowsOp → filterOp? → seqScanOp
```

`findScanLeaf` traverses `filterOp` and `projectOp` wrappers until it reaches
`seqScanOp`, which implements `currentTID()`.

For a partitioned table the planner produces a UNION ALL over leaf SeqScans:

```
lockRowsOp → filterOp? → setOp (UNION ALL)
                             ├── projectOp → seqScanOp(leaf_1)
                             └── projectOp → seqScanOp(leaf_2)
```

`findScanLeaf` hit the `default` arm on `setOp` and returned `nil`.
`drainAndStamp` then set `haveTID = false` for every row, and the stamp pass
was a no-op — every leaf tuple kept `xmax = InvalidTransactionID`.

### Consequence

`upsertOp.findInProgressConflict` (Cases 2 and 3) checks the heap tuple's
xmax to detect a concurrent lock or update on a conflicting row. With the
xmax unstamped, Case 3 (lock-only xmax) never fired for a concurrent
`SELECT … FOR UPDATE`, and the upsert proceeded without waiting — violating
`INSERT … ON CONFLICT DO UPDATE` concurrency semantics on partitioned tables
(`TestPort_IsolationInsertConflictDoUpdate4`).

## Fix

Two targeted changes, same package:

### 1. `setOp.currentTID()` — `internal/executor/operators_setop.go`

`setOp` now implements `currentTIDProvider`. After each `setOp.Next()` call
the just-yielded row came from the left child while `!leftDone`, and from
the right child once `leftDone = true`. `currentTID()` delegates to
`findScanLeaf(active_child)` to reach the correct leaf seqScan and return its
`(rel, ptr)`.

For nested setOps (N > 2 partitions) the delegation is recursive: an outer
setOp whose left child is another setOp calls `findScanLeaf(left)` which
now returns the inner setOp, and `inner.currentTID()` recurses one more
level — reaching the correct seqScanOp regardless of depth.

### 2. `findScanLeaf` — `internal/executor/operators_lockrows.go`

Added `case *setOp: return v` so the traversal stops at a setOp and returns it
as the `currentTIDProvider`, rather than falling through to `nil`.

## Correctness

- Non-partitioned tables: `findScanLeaf` short-circuits at `seqScanOp` or
  `indexScanOp` before reaching a setOp — no change.
- Two-partition tables: single setOp; `currentTID()` delegates to the active
  Project → seqScanOp leaf.
- N-partition tables: recursion through nested setOps reaches the correct
  seqScanOp.

## Test

`TestLockRowsStampsXmaxOnPartitionedTableLeaf`
(`internal/executor/operators_lockrows_test.go`):
creates a two-table (parent + one leaf) partitioned fixture, inserts a row
into the leaf, runs `SELECT * FROM parent FOR UPDATE`, and asserts that the
leaf tuple's `Xmax == ctx.Tx.XID` and `HeapXmaxLockOnly` is set.

End-to-end confirmation: `TestPort_IsolationInsertConflictDoUpdate4` flips
from SKIP (deferred) to PASS.

## Known limitations / non-goals

- `indexScanOp` is also a `currentTIDProvider` but partition scans always use
  seqScan today; index-scan-over-partitioned-table is a future follow-up.
- AFTER-stamp checks (e.g. lockmgr `acquireTupleLock`) are also inside
  `stampLock`; this fix unblocks that path too, so tuple-level lock tags are
  now correctly registered for partitioned-table rows.
