(idle — nothing in flight)

## Loop summary (2026-07-12, loop #69)

**Nightly triage:** action-items batch `20260711-011536` (unchanged since #58) —
all 3 AI items already `[x]` in M-NIGHTLY. No new nightly work.

**Task — unimplemented_feat #135 (pg_get_expr, indexprs heap slice) — RESOLVED.**
Closed the last open piece: the heap-persisted `pg_index.indexprs` was always
NULL because `DecodePGIndexPhysicalRow` inferred `indpred` presence from
remaining byte length (only unambiguous while indexprs is NULL — two consecutive
nullable pg_node_tree varlenas can't be told apart without the tuple null bitmap).

Landed (files):
- internal/catalog/codec.go: DecodePGIndexPhysicalRow(data, bitmap []byte) is now
  null-bitmap-aware (probes bits 19/20); new PGIndexRow.IndExprs/IndHasExprs;
  helpers pgIndexBitNotNull (nil bitmap ⇒ present), pgIndexVarlenaLen.
- internal/executor/pg18_user_catalog_rows.go: buildUserPGIndexRow emits indexprs
  via catalog.IndexExprsText (was hardcoded NullDatum).
- internal/initdb/open.go: recovery caller passes ht.Bitmap.
- internal/executor/operators_ddl.go: resyncIndexHeapRow matcher passes nil
  (matches only fixed-offset indexrelid).
- Test TestBuildUserPGIndexRowExprPredNullBitmapRoundTrip (all 4 NULL combos).
- docs/design/0122-0019-pg-index-node-tree-columns.md Follow-up(2026-07-12);
  README row; deferral_ledger.md (2 rows flipped resolved + 1 new resolved row);
  unimplemented_feat.json #135/#57 → resolved.

Backward-compatible: legacy rows always carried a bitmap (indexprs was always
NULL). Alignment verified (both pg_node_tree → 4-align, encoder always pads,
PG-standby-readable).

Gates: go build clean; catalog+executor+initdb(CreateIndex) tests PASS;
tpch-spotcheck PASS (Q12=2/Q13=33); ralph-state-guard consistent (repaired prev
clean-exit marker); pgbench smoke via pre-commit hook (see commit).

Deferred (new ledger row): initdb heap-scan recovery (loadUserIndexesFromHeap)
still rebuilds expression indexes via WAL replay, not from recovered IndExprs
text — separate larger recovery feature.

In-flight: none
