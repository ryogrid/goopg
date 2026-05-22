# M0100-0005 Loop-22: MERGE EPQ Delete Chain Follow + RETURNING Propagation

**Status**: accepted  
**Filed**: 2026-05-20  

## Problem

Two related bugs in `internal/executor/operators_merge.go` caused incorrect MERGE
behaviour after concurrent UPDATEs in isolation permutations tracked by
`TestPort_IsolationMergeMatchRecheck`.

### Bug A — `mergeApplyDelete` misidentified UPDATE as DELETE

`mergeApplyDelete`'s EPQ retry path contained:

```go
if ctx.TxnMgr != nil && !ctx.TxnMgr.HasAbortedXID(xmax) {
    return errMergeSourceUnmatched // wrong: also fires for committed UPDATEs
}
```

Any committed xmax was treated as a DELETE, returning `errMergeSourceUnmatched`
immediately. When the concurrent transaction was an UPDATE (not a DELETE), the
HOT chain led to a live successor, but the code never followed it. The MERGE then
treated the source row as NOT MATCHED and did nothing — the row survived
un-deleted even though the re-evaluated WHEN clause would have matched DELETE.

Fix: remove the early return, call `mergeEPQRefreshSnap(ctx)` (mirroring
`mergeApplyUpdate`), then follow HOT chain → non-HOT chain → only return
`errMergeSourceUnmatched` when no successor is found (true DELETE or
cross-partition move via the sentinel check).

### Bug B — `applyMod` passed `mergePendingMod` by value

`applyMod` had signature `func (..., mod mergePendingMod) (applied bool, error)`.
Inside the EPQ retry loop, after re-evaluating the WHEN clause, the corrected
values were stored in the local `mod` copy:

```go
mod.newRow = newRow  // balance=100 after EPQ recheck
```

But `mod` was a local copy. Back in `mergeOp.Next()`, the caller still held the
original `mod` with `newRow.balance = 640` (the pre-EPQ WHEN-2 result):

```go
applied, err := o.applyMod(modRel, modTbl, n, mod)  // mod unchanged after return
...
o.collectReturningRow(mod.newRow)  // uses balance=640 ← BUG
```

The heap was written correctly (balance=100 by `mergeApplyUpdate`), but RETURNING
materialised the wrong value.

Fix: change signature to `*mergePendingMod`; change loop from
`for _, mod := range mods` to `for i := range mods { mod := &mods[i] }`. All EPQ
modifications to `mod.newRow`, `mod.blk`, `mod.slot`, `mod.action`, and
`mod.tgtRow` now propagate to the caller's RETURNING collector.

## Impact

- `update1 merge_delete c2 select1 c1` permutation: row now correctly deleted (0 rows).
- `update_bal1_tg merge_bal_tg c2 select1_tg c1`: RETURNING output now shows
  `balance=100` (WHEN-1 recheck result) instead of `balance=640` (original
  WHEN-2 computation).
- `MergeMatchRecheck` first divergence: L262 → L416 (415/503 lines match).

## Remaining gap (L416)

The moved-partition sentinel is not stamped when `update1_pa_move` runs through
the `updateViaIndex` path (B-tree IndexScan). `updateViaIndex` does not call
`PageSetHeapTupleMovedPartition`; cross-partition detection is only in the
`updateOp.Next()` SeqScan path. `epqSlotMovedToAnotherPartition` then returns
false, the MERGE falls through to `errMergeSourceUnmatched` (DO NOTHING), and
the expected 0A000 error is never raised. This is a separate scope fix.

## Files modified

- `internal/executor/operators_merge.go`: `mergeApplyDelete`, `applyMod`
