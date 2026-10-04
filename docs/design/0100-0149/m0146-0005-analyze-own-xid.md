# ANALYZE counts its own transaction's rows (M0146-0005 filed recon)

Status: landed 2026-09-27. Root task **DONE 2026-10-04**, closed with slice 116: every first-divergence record at both scales routes to a named task (`analysis/m0146/m0146-0005/routing-20261004b/ROUTING.md`). Re-opened 2026-10-03 after the S4 hold (owner).
Evidence: `analysis/m0146/m0146-0005/analyze-own-xmin/`.
Fix-plan item: the unnamed `[ ]` entry "The executor test fixture's ANALYZE
records `RowCount: 0` for rows it cannot see" (filed 2026-09-25 by
M0146-0005c).

## Defect

```sql
BEGIN;
CREATE TABLE t(k int);
INSERT INTO t SELECT g FROM generate_series(1,100) g;
ANALYZE t;
SELECT reltuples FROM pg_class WHERE relname='t';   -- goopg 0, PG 18.3 100
COMMIT;
```

The recon item asked whether the fixture's `RowCount: 0` was a
fixture-transaction artefact or a real engine divergence. The live
reproduction on a real server shows the latter: same-transaction
`SELECT count(*)` saw the 25 own rows — the general MVCC own-xid credit
already worked — while `ANALYZE` recorded 0.

## Root cause

`analyzeRelationWith` (internal/executor/operators_analyze.go) opens a
nested `transam` transaction purely to mint its scan snapshot, then passed
**the nested tx's XID** as `currentXID` to `transam.TupleVisible`. That
argument is the "self" identity driving `HeapTupleSatisfiesMVCC`'s own-xmin
arm (`h.Xmin == currentXID`, visibility.go). The nested analyze tx writes
nothing, so no tuple ever matched — every row the session transaction
inserted fell through to `snap.SeesCommittedXID`, which reports an
in-progress xid as invisible. Uncommitted own inserts, and by symmetry
own deletes, were invisible to statistics collection.

## Fix

`analyzeRelationWith` now carries `ownXID`, defaulting to the nested tx's
XID and replaced by `dsCtx.Tx.XID` — the session transaction — when
`mgr.IsXIDActive(dsCtx.Tx.XID)` holds.

- **`IsXIDActive` is the guard, not bare non-nil.** It mirrors PG's
  `TransactionIdIsCurrentTransactionId`: the own arm applies only while
  the caller's transaction is still open. Executor fixtures that commit
  their write tx before analyzing (`seedRowsAndAnalyze` and siblings)
  must NOT take the own arm — a committed xid is judged by the snapshot,
  and crediting it would re-hide committed rows behind the cmin/curcid
  command-ordering check.
- **`dsCtx == nil` keeps the nested XID.** Autovacuum's
  `AnalyzeRelationSampled` and the test-only `analyzeRelation` wrapper
  have no session transaction; PG's autovacuum worker likewise analyzes
  in its own transaction and sees only committed rows.
- **`curcid`/`combo` were already the session's** (`dsCtx.CmdID`,
  `dsCtx.comboStore()`). Crediting the session XID finally pairs them
  with the xid space the cmin/cmax stamps were written under — the
  previous pairing (session command ids vs nested-tx xid) was
  meaningless.

The cmin/curcid rule still hides rows stamped by the **current** command:
PG's own-command ordering, not a bug. Rows written and analyzed at the
same command id stay invisible.

## Verified vs PG 18.3 (scratch clusters, byte-identical)

| step | goopg | PG 18.3 |
|------|-------|---------|
| `INSERT 100; ANALYZE` (same txn) | 100 | 100 |
| `DELETE k>50; ANALYZE` (same txn) | 50 | 50 |
| post-commit `reltuples` | 50 | 50 |

## Regression test

`TestAnalyzeSeesOwnUncommittedInserts`
(internal/executor/operators_analyze_test.go): INSERT without commit →
`advanceStmtCounter` (the fixture's CommandCounterIncrement stand-in) →
`analyzeRelationCtx` → `RowCount == 25`.

## Residual / notes

- Rows written and analyzed at the same command id remain invisible by
  PG's own-command rule; fixture tests needing counted rows advance the
  statement counter (as `runDDL` already does), or pin stats via
  `setFixtureStats` (M0146-0005c).
- The analyze scan's snapshot still comes from a fresh ReadCommitted
  nested tx rather than the session's snapshot. Under READ COMMITTED
  this is observably identical at statement granularity; PG's ANALYZE
  inside a REPEATABLE READ / SERIALIZABLE block uses the transaction
  snapshot — a possible residual divergence, unobserved in the gate
  corpora.
