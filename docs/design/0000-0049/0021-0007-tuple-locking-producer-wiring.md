# 0021-0007 — Tuple-Level Locking Producer Wiring

**Status:** accepted (step 2a — lockRowsOp stamps per-row lock-only
xmax + emits WAL; INSERT/UPDATE/DELETE conflict detection deferred)
**Milestone:** [0021 — Pessimistic Row Locking](../../milestones/0021-pessimistic-lock-select-for-update.md)
(deferred follow-up: tuple-level locking on top of M0012).
**Spans seam:** Storage Pool LogHeapLock hook, initdb wiring,
seqScanOp currentTID, lockRowsOp two-pass drain-then-stamp.
**Cross-links:**
[0021-0005](0021-0005-tuple-level-locking-storage-and-mvcc.md)
(storage primitives — step 1),
[0021-0006](0021-0006-tuple-locking-heap-lock-wal.md) (xl_heap_lock
WAL record — step 3).

## Context

Steps 1 and 3 produced the storage primitives + the WAL record
catalog entry. This slice is the **producer**: SELECT FOR UPDATE
(lockRowsOp) now actually stamps `HeapXmaxLockOnly` xmax on each
yielded row's underlying heap tuple AND emits the row-lock WAL
record so crash recovery can re-apply it. Until this slice landed,
M0021's executor was relation-coarse only — RowShareLock at the
relation level, no per-tuple state.

## Filename note

Continues the `0021-00NN-...` numbering started in step 1 even
though this is past the original M0021 milestone 0001-0004
doc-set; the numbering is for documentation sequencing, not run
tracking.

## Pool LogHeapLock hook

Adds `LogHeapLockFunc` + `Pool.LogHeapLock()` accessor + `PoolConfig.LogHeapLock`
field, mirroring the existing LogHeapInsert / LogHeapDelete /
LogHeapVacuum pattern. `initdb.Open` wires a closure that calls
`wal.EncodeHeapLock` + `walWriter.Append`, parallel to its
`logHeapDelete` / `logHeapVacuum` siblings:

```go
logHeapLock := func(rel storage.RelFileNode, blk storage.BlockNumber, lineSlot uint16, xmax storage.TransactionID, lockStrength uint16) (storage.LSN, error) {
    payload := wal.EncodeHeapLock(rel, blk, lineSlot, xmax, lockStrength)
    _, end, err := walWriter.Append(payload)
    if err != nil { return 0, err }
    return storage.LSN(end), nil
}
```

`PoolConfig.LogHeapLock` is wired into the same `NewPool` call as
the other emitters; nil disables the optimisation and
`MarkDirtyChangeRecord` falls back to FPI emission for crash
safety (mirrors the existing pool-hook fallback contract).

## seqScanOp.currentTID

The scan tracks `(curBlock, curSlot)` while iterating line
pointers. `Next()` increments `curSlot` _after_ fetching, so the
slot of the most recently returned row is `curSlot - 1`. New
helper:

```go
func (o *seqScanOp) currentTID() (storage.RelFileNode, storage.ItemPointer, bool) {
    if o.pinned == nil || o.curSlot == 0 {
        return storage.RelFileNode{}, storage.ItemPointer{}, false
    }
    rel := o.ctx.Catalog.RelFileNode(o.plan.Table)
    return rel, storage.ItemPointer{Block: o.curBlock, Offset: o.curSlot - 1}, true
}
```

Caller invokes between Next-returns-row and the next Next call —
the `(block, slot)` pair stays valid until the scan advances past
the page.

## lockRowsOp two-pass drain-then-stamp

The challenge: `seqScanOp` holds the page's `RLock` continuously
across multiple Next() calls (`slot.RLock()` fires once per page
on first access; `RUnlock` only when the slot range exhausts).
While the scan holds the RLock we cannot grab the same slot's
write `Lock()` for xmax stamping — Go's `sync.RWMutex` would
deadlock waiting for readers to release.

Solution: two-pass drain-then-stamp. lockRowsOp's first Next call:

1. Drain the entire child chain, recording `(rel, ItemPointer, row)`
   per row. TID is captured inline via `findSeqScan(child).currentTID()`
   AFTER each child.Next() returns and BEFORE the next child.Next()
   call (so the seqScan's curBlock/curSlot is authoritative).
2. After child returns EOF, the seqScan has released all page
   RLocks via `releasePinned`.
3. Stamp pass: for each pending row, pin the page, write-Lock,
   `PageSetHeapTupleLockOnly`, mark dirty through
   `MarkDirtyChangeRecord(LogHeapLock)`, unlock, unpin.

Subsequent Next() calls return rows from the buffer until EOF.

```go
type lockRowsOp struct {
    plan         *planner.LockRows
    child        Operator
    scan         *seqScanOp     // resolved at Open via findSeqScan
    lockStrength uint16         // ExclLock / ShrLock
    pending      []pendingLockedRow
    pos          int
    drained      bool
}
```

Memory: pending buffer holds the result set. SELECT FOR UPDATE
typically targets a small range so this is acceptable for
Stage A. Per-tuple streaming stamping requires a deeper seqScan
refactor (one Pin/RLock per Next instead of per-page) and is
deferred.

## findSeqScan unwrap helper

```go
func findSeqScan(op Operator) *seqScanOp {
    for {
        switch v := op.(type) {
        case *seqScanOp: return v
        case *projectOp: op = v.child
        case *filterOp:  op = v.child
        default:         return nil
        }
    }
}
```

Walks past the typical `Project → Filter → SeqScan` chain. nil
return (e.g. IndexScan, Values, CTEScan) means lockRowsOp falls
through to pass-through Next; only the relation-level lock
acquired at Open applies. IndexScan-driven SELECT FOR UPDATE
support is a follow-up — needs an `indexScanOp.currentTID`
twin and the find-helper extended to traverse into it.

## Lock strength selection

```go
if len(o.plan.Locks) > 0 {
    switch o.plan.Locks[0].Strength {
    case planner.LockStrengthForShare:
        o.lockStrength = storage.HeapXmaxShrLock
    default:
        o.lockStrength = storage.HeapXmaxExclLock
    }
}
```

v0 supports a single per-LockRows strength; multi-clause merge
under strongest-wins is deferred (the planner already produces
duplicate `LockedRel` entries for that case; a future slice
folds them).

## Tests

`internal/executor/operators_lockrows_test.go`:

- `TestLockRowsStampsTupleLockOnlyXmax` — NEW. Runs `SELECT id
  FROM items FOR UPDATE` end-to-end through
  parser→analyzer→planner→executor; reads the heap page back
  and verifies every row stamped through lockRowsOp carries
  `Xmax == ctx.Tx.XID` + `HeapXmaxLockOnly` + `HeapXmaxExclLock`
  bits set.
- All five pre-existing M0021 executor tests continue to pass.

Full `go test ./...` green.

## Out of scope

- INSERT / UPDATE / DELETE detection of foreign lock-only xmax
  (the conflict-blocking path that makes "another xact holds a
  row lock" actually block). Next slice.
- IndexScan currentTID + lockRowsOp traversal into indexScanOp.
- MultiXact-aware multi-holder support for FOR SHARE.
- Streaming per-row stamping (eliminate the two-pass buffer) —
  requires seqScanOp Pin/RLock-per-row refactor.
- pg_locks-style introspection of tuple-level lock holders.
