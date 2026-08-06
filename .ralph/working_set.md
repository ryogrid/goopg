(idle — nothing in flight)

Last loop: **M0125-0048 CLOSED and committed** — grouping sets are no longer a
UNION ALL. ONE `Aggregate` node, one hash table per set, one pass over the
child.

1. `GroupExprs` = deduplicated union of the sets (PG's `parse->groupClause`),
   `GroupingSets [][]int` = the slots each set keeps, `GroupingMasks [][]int64`
   = each `GROUPING(...)` call's bitmask per set.
2. **The mask is an output COLUMN, not an expression over a hidden set-id.**
   It depends only on which set produced the row, so `GROUPING(a,b)` resolves
   to a plain `ColumnRef` — no new Expr node, no evaluator case, no EXPLAIN
   formatter. Bonus: the column is named `grouping`, as PG names it.
3. **The ordinary aggregate is the one-set case, not a second operator**
   (`sets = [[0..n-1]]`; set prefix omitted when there is one set). A separate
   operator would have cloned 250 lines of shared-state/`finishAgg` logic.
4. **What the oracle taught it:** PG proves a functional dependency only
   against the INTERSECTION of the sets (`gset_common` →
   `groupClauseCommonVars`, `parse_agg.c parseCheckAggregates`), so
   `SELECT id, name … GROUP BY ROLLUP(id)` is 42803 even with `id` a PK.
5. Retired outright: `rewriteGroupingSets`/`substituteGroupingExpr`/
   `groupingBitmask`, `groupingsets_share.go` + both its tests, and
   `GOOPG_GS_SHARE_SOURCE` (kept as `retired(M0125-0048)` in
   `flagProvenanceRetired` so older artefacts stay attributable).

Files: `internal/planner/{groupingsets,planner,plan,parallel_agg,flaglabels}.go`,
`internal/executor/{operators_join_agg,operators_explain,explain_cte}.go`,
`internal/parser/{ast,expr}.go`, `internal/analyzer/analyzer.go`,
`internal/executor/grouping_sets_single_pass_test.go` (8 new tests, every value
from live PG 18.3 on 65432), `scripts/planner-flags.env` (regenerated),
`docs/design/0125-0048-single-pass-grouping-sets.md` + README index (and the
-0040 row marked RETIRED), fix_plan (-0048 ticked), 4 ledger rows.

Gates run: units gate PASS; planner/executor/parser/analyzer PASS; full
`go test ./...` PASS except the pre-existing `TestPort_IsolationEvalPlanQual`
(nightly `AI-20260806-011323-001`, unrelated and unchanged);
`scripts/tpch-spotcheck.sh` Q12=2/Q13=35 canonical PASS; SF0.5 plan channel
`queries=99 same=91 changed=8` — the eight ARE exactly the eight ROLLUP queries
(Q5 Q14 Q18 Q22 Q27 Q67 Q77 Q80), no other query moved; SF0.5 sweep of those
eight `PASS=8 MISMATCH=0` (Q67 82s→17s, Q18 37s→9s, Q22 21s→4s, Q27 31s→10s);
pgbench smoke via the commit hook. Plan baseline is the newest capture
`plans-20260806-185158.txt`; only `oracle.txt` is git-tracked and it is
unchanged.

NEXT LOOP (banner: M0124 closed → M0125 → M0127 → M-NIGHTLY → M0123).
`ci/logs/action-items.md` was still run `20260806-011323` this loop (all 18
filed, nothing new) — re-check first; a NEW nightly at `status: pass` makes
M0127-P6.1 selectable. Open M0125 items are now only `M0125-0003` (staged
`GOOPG_RELSIZE_FALLBACK`, stages 1+2 landed default-off) and
`-0031/-0032/-0033/-0041`, all four marked `[→ M0127: absorbed]` — so M0125 is
effectively down to -0003, and M0127 is the next live milestone.

In-flight: none. The PG reference cluster on 65432 was started for oracle
captures and stopped again (`bench/tpch/setup_pg.sh` to bring it back;
`set -a; source bench/tpch/env.sh; set +a` for PGPASSWORD — a bare psql blocks
on a password PROMPT and will eat a 15-minute Bash timeout).
