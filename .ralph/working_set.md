(idle — nothing in flight)

Loop #40 COMPLETE: M0118-0009 horizons EXPLAIN enabler ADDED
(design 0118-0102 — NOT a spec promotion). The next horizons rung after
0118-0101 (EXECUTE INTO STRICT).

What landed (all internal/executor, EXPLAIN infrastructure):
- operators_explain.go: `EXPLAIN (FORMAT JSON)` now nests the plan tree under
  a top-level "Plan" key (PG-faithful) — `[ { "Plan": {root}, "Planning
  Time":…, "Execution Time":… } ]`. goopg flattened the node into the array
  element before, so horizons' `…->0->'Plan'->…` returned NULL. Both JSON
  paths (plain + ANALYZE) wrap.
- operators_explain.go: describePlan/describePlanVerbose render
  `Index Only Scan using <idx> on <table>` (was %T default).
- instrument.go: nodeStats.heapFetches + `heapFetchCounter` interface;
  maybeInstrument hands the IOS &stats.heapFetches.
- operators_indexonly.go: indexOnlyScanOp.heapFetchCount ++ per non-ALL_VISIBLE
  fallback entry (mirrors ioss_HeapFetches).
- operators_explain.go: planToJSONWithStats emits "Heap Fetches" JSON key +
  walkPlanAnalyze emits "Heap Fetches: N" text line, IOS-only, ANALYZE-only.
- Tests: new explain_heap_fetches_test.go (2 before VACUUM / 0 after, IOS
  label, text line); updated 6 internal JSON tests for the "Plan" wrapper.
- docs/design/0118-0102 + README; fix_plan note + ledger row.

RESIDUAL horizons BLOCKER (isolated this loop via live re-probe — actual ""):
goopg's PLANNER emits `Sort → Seq Scan` (NOT an IOS) for `SELECT * FROM
horizons_tst ORDER BY data` and does NOT honor `enable_seqscan/indexscan/
bitmapscan=false`, so no IOS node → no Heap Fetches key → navigation NULL.

NEXT (Effort-L, in order):
(1) PLANNER: honor enable_seqscan/indexscan/bitmapscan GUCs + promote an
    ordered full-index scan to IndexOnlyScan when the index provides ORDER BY
    and covers the projection (today IOS promotion needs an equality/range
    IndexScan child via tryPromoteIndexOnlyScan; a bare ORDER BY full scan
    stays Sort→SeqScan). Only then does Heap Fetches become non-NULL.
(2) MVCC pruning-horizon core: IOS heap-fetch counts reflecting opportunistic
    pruning + prune/VACUUM respecting a concurrent older snapshot for
    permanent vs temp tables (perms 1/3/4 expected to match with this loop's
    IOS infra, 2/5 differ on temp prunability).

Other remaining M0118-0009 (all Effort-L): intra-grant-inplace (pg_class
xmax-wait), stats (pg_stat_force_next_flush + cumulative stats), prepared-
transactions{,-cic} (2PC).

Gates: TestExplainHeapFetchesIndexOnlyScan + full internal/executor +
internal/planner PASS; build+vet clean; pgbench smoke = pre-commit hook.
