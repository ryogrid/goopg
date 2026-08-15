# Working set — M0134-0001 P2 S4 (class 8: index range comparison op)

**Task:** M0134-0001 aggregates.sql — S4 (class 8) LANDED. goopg rendered
`Index Cond: (c2 <= 100)` + redundant `Filter: (c2 < 100)` where PG renders only
`Index Cond: (c2 < 100)`. Fixed via: store original op + exclusive btree scan +
render original op + conservative Filter-drop.

**Status:** LANDED + committed + pushed. Code commit `aa40caa6`, docs/ledger commit
(following). aggregates.diff **1387→1385**, zero PASS→FAIL.

**Files (this loop):** `internal/planner/plan.go` (`IndexScan.LowOp/HighOp`),
`internal/planner/planner.go` (`tryRangeIndexScan` op capture + `isPlainConstantBound`
+ Filter-drop gate; `tryPromoteIndexOnlyScan` exclusive-bound guard),
`internal/access/btree/btree.go` (`rangeScanPos` exclusive lo/hi stop),
`internal/executor/operators_index.go` (thread lo/hiExclusive),
`internal/executor/operators_explain.go` (`formatIndexCond` renders stored op),
`internal/executor/operators_bitmap.go` + lpdead tests (default-inclusive callers),
tests (`range_exclusive_index_scan_test.go` new + 2 `range_index_scan_test.go` +
`exprwalk_inventory_test.go` pin), `docs/design/0134-0001-p2-explain-format.md`,
`.ralph/deferral_ledger.md` (2 rows).

**Key symbols:** `tryRangeIndexScan` (planner.go:9379-9510), `rangeScanPos`
(btree.go:3873-3992), `formatIndexCond` (operators_explain.go:851-886),
`tryPromoteIndexOnlyScan` (planner.go:12951), `isPlainConstantBound` (planner.go).

**Hypothesis/Findings:**
- **Design-doc correction landed:** the class map's "drop the redundant Filter"
  premise was WRONG — goopg's btree is inclusive-only, so the Filter was
  executor-necessary. The faithful fix = exclusive scan (not a render-only lie).
- **Filter-drop is deliberately conservative:** drops only when WHERE is a single
  conjunct == the folded range conjunct AND index is single-column AND the bound is
  a plain literal/param (no volatile FuncCall). Composite index (`btg_y_x_w_idx`) and
  volatile bound (`random()`) keep the Filter — 4 row-parity tests pin this.
- **Option B over A for IOS:** `indexOnlyScanOp` is inclusive-only AND renders no
  Index Cond/Filter, so a promoted IOS would leak the boundary row AND still be
  EXPLAIN-divergent. Refuse promotion for exclusive bounds (ledger row 1); making IOS
  exclusive-aware is Option A scope (deferred).
- Reviewer (APPROVE-WITH-NITS) caught the volatile + composite-blob leak; both fixed
  in round 3.

**Next step — M0134-0001 remaining slices (in order):** (1) **S6 e.2** inheritance/
MergeAppend (blocks 15/16, now unblocked by e.1 partial-index fix); (2) the hard **(d)**
scalar-subquery d-rewrite (correlation gate + deparse + numbering — coupled, do NOT
re-attempt standalone d-render); (3) **S8/S9** cost-model (presorted agg + join shape);
(4) **S3** join-label `(INNER)`/`(CROSS)` (7 lines, formatter). S7 residuals
(GROUPING() guard + index tie-break) low priority.

**Gates run (this loop):** `go test ./internal/{planner,executor,access/btree}` PASS;
`scripts/pg-regress-runner.sh aggregates` 1387→1385 (zero PASS→FAIL);
`scripts/tpch-spotcheck.sh` PASS ×2 (Q12=2/Q13=35, round-2 + final round-4);
`RALPH_PRECOMMIT_SCOPE=units` PASS; pre-commit pgbench smoke PASS (0 failed).

**Delegation:** implementer `0134-0001-s4-class8-indexcond` DONE (3 rounds: core →
Option-B decision → reviewer tightenings). researcher
`0134-0001-s4-class8-indexcond-research` DONE. reviewer + tester DONE.

**In-flight:** none.
