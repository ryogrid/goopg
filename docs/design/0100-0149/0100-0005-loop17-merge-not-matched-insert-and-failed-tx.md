# M0100-0005 Loop-17: MERGE NOT MATCHED INSERT + Failed Transaction State

## Summary

Four fixes to advance the RC isolation suite from 11/21 to 12/21 PASS.

## (A) MERGE NOT MATCHED INSERT — full insertOp parity

`mergeOp.Next()` NOT MATCHED INSERT path now:
1. Routes to the correct partition child via `routeToPartition` + `remapRowForPartition`
2. Fires BEFORE INSERT triggers via `fireTriggers` (matching `insertOp` behaviour)
3. Checks unique constraints with wait semantics via `checkUniqueIndexesForInsert`
   (waits for concurrent in-flight inserts, raises 23505 on commit, retries after abort)
4. Maintains btree indexes via `maintainUniqueIndexesForInsert`

## (B) MERGE MATCHED UPDATE EPQ "row gone" → NOT MATCHED fallback

`mergeApplyUpdate` now returns a distinct `errMergeSourceUnmatched` sentinel when
`epqFollowHOT` returns not-found (target row deleted by concurrent committed
transaction). The outer loop in `mergeOp.Next()` resets `srcRows[mod.srcIdx].matched=false`
so the NOT MATCHED clauses can fire on the source row. `srcIdx int` field added to
`mergePendingMod` to identify which source row to reset.

## (C) isLiveForUniqueCheck — post-snapshot abort detection

Added `case ctx.TxnMgr.HasAbortedXID(xmin)` before the `default` arm so
transactions that aborted after our snapshot was taken are correctly classified
as non-live (previously fell into `default` → live, causing spurious 23505 when
MERGE NOT MATCHED INSERT waited for a concurrent aborter then found no conflict).

## (D) Failed-transaction state (25P02)

`connTxState` gains `failed bool` with `Fail()` and `IsFailed()` methods, reset
in `Begin()` and `End()`. Any `errQueryErrorSent` that occurs while
`!autoCommit && connTx.InExplicit()` calls `connTx.Fail()`. Subsequent statements
in the failed block return `25P02 "current transaction is aborted"`. COMMIT on a
failed block silently ROLLBACKs via `TxnMgr.Rollback` and calls `connTx.End()`.
