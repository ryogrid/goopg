Loop #41 COMPLETE: M0118-0009 horizons ENABLER ADDED (design 0118-0103 — NOT a
spec promotion). Closes the PLANNER half of the horizons ladder. Next is the
final rung: the Effort-L MVCC pruning-horizon core.

What landed (ordered IndexOnlyScan under `enable_seqscan = off`):
- internal/catalog/catalog.go: SearchPathCatalog.DisableSeqScan field +
  SeqScanDisabled() accessor (mirrors TempOwnerToken/CurrentTempOwner pattern).
- internal/server/dispatch.go: sessionPlanCatalog + ctxPlanCatalog read
  enable_seqscan GUC (normalises to "on"/"off") → set wrapped.DisableSeqScan.
- internal/planner/planner.go: currentSeqScanDisabled (catalog-wrapper Unwrap()
  walk) + tryPromoteOrderedIndexOnlyScan — replaces Project(Sort(SeqScan)) with
  an unbounded IndexOnlyScan (nil Key/Keys/bounds → full-range RangeScan ascending,
  Sort dropped) when a non-partial btree index's leading key cols match the
  ASC/NULLS-LAST ORDER BY keys AND key+INCLUDE cover the projection. Wired as
  `else if` after tryPromoteIndexOnlyScan in planSelect's promotion block.
- internal/planner/ordered_indexonlyscan_test.go (NEW): 3 tests (promoted when
  disabled / not promoted by default / DESC not promoted). NB filename gotcha:
  `*_ios_test.go` is a GOOS=ios build constraint — never name a Go file `_ios`.
- docs/design/0118-0103 + README index; fix_plan note + ledger row.

RESIDUAL horizons BLOCKER (isolated via live re-probe — only 3 lines differ now):
The IOS plan + `…->0->'Plan'->'Heap Fetches'` navigation now MATCH. Remaining:
- L125 expected 0 / actual 2 — TEMP table: deleted rows SHOULD be prunable
  despite a concurrent older snapshot; goopg does NOT opportunistically prune the
  temp heap during the IOS.
- L244 + L254 expected 2 / actual 0 — PERMANENT table: VACUUM must NOT remove
  rows still visible to lifeline's older RR snapshot; goopg VACUUM ignores the
  concurrent OldestXmin horizon → removes them.

NEXT (Effort-L, the spec's actual subject): MVCC pruning-horizon core —
opportunistic prune during the IOS + VACUUM cutoff that respects the global xmin
horizon (GlobalVisHorizon / vacuum_get_cutoffs OldestXmin) for PERMANENT
relations but treats TEMP relations as always-prunable (the temp short-circuit).
Separate MVCC change → race gate `go test -race ./internal/mvcc/... ./internal/wal/...`.

Other remaining M0118-0009 (all Effort-L): intra-grant-inplace (pg_class
xmax-wait — runtime shared-catalog MVCC-tuple row locks), stats
(pg_stat_force_next_flush + cumulative stats), prepared-transactions{,-cic} (2PC).

Gates run: TestOrderedIndexOnlyScan{Promoted…,NotPromotedByDefault,RequiresAscending}
+ full internal/planner + internal/catalog + internal/executor + internal/server
PASS; build+vet+gofmt clean; horizons re-probe (IOS plan matches); pgbench smoke =
pre-commit hook.
