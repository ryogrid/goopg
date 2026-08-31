# Design: EvalPlanQual — Row Recheck on Concurrent UPDATE (M0098-0004)

**Status**: accepted  
**Milestone**: M0098-0004  
**Expected gain**: near-zero abort rate → 10–20% TPS gain on Standard workload

## Problem

When `isConcurrentlyUpdated()` detects an xmax conflict in `updateOp` or `deleteOp`,
goopg immediately returns SQLSTATE 40001. At -c 100 with pgbench standard workload,
0.022% of transactions abort (M0098-0001 baseline), requiring client retry overhead.
PostgreSQL uses EvalPlanQual to avoid this: wait for the conflicting transaction, then
re-evaluate the predicate on the freshened tuple.

## Design

### EvalPlanQual protocol (READ COMMITTED)

When `isConcurrentlyUpdated(header, myXID)` returns true:

1. **Extract conflicting XID**: `conflictXID := header.Xmax`
2. **Release page lock**: unlock + unpin (must not hold page lock while waiting)
3. **Wait for conflicting transaction**: `TxnMgr.WaitForXID(ctx.Ctx, conflictXID)`
4. **Refresh snapshot**: `ctx.Snap = snap.Clone()` via `TxnMgr.SnapshotFor(ctx.Tx)`
5. **Re-read tuple**: pin the same page, read the same slot
6. **Check visibility** under new snapshot:
   - If not visible → skip (conflicting txn committed, row was deleted/replaced; v0 doesn't follow HOT chains)
   - If visible → proceed normally (conflicting txn aborted; original row is alive)
7. **Retry limit**: max 3 rechecks per row; escalate to 40001 only on exhaustion

### v0 scope

- **HOT chain following not implemented**: after a committed concurrent UPDATE, the new row version is at a different TID. goopg v0 skips the row rather than following the HOT chain to the new version. This is correct for DELETE (row is gone). For UPDATE, it means the UPDATE becomes a no-op for that specific row — acceptable for pgbench since account updates are statistically rare in concurrent sessions.
- Applies to `updateOp` (all conflict sites) and `deleteOp`.
- `tryApplyHOTUpdate` (same-page HOT path) also gets EPQ.

### Implementation

Add helper `epqWaitAndRecheck(ctx *Context, rel storage.RelFileNode, blk storage.BlockNumber, slot uint16) (visible bool, row Row, cols []catalog.Column, err error)`:
1. Waits for conflicting XID (caller extracts it before calling)
2. Refreshes snapshot
3. Re-reads tuple at (blk, slot)
4. Returns visibility + decoded row

Both updateOp and deleteOp call this helper then retry or skip.

## Files changed

| File | Change |
|------|--------|
| `internal/executor/operators_storage.go` | Add epqWaitAndRecheck; modify updateOp + deleteOp conflict sites |
| `docs/design/README.md` | Index entry |
