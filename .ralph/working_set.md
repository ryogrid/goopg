(idle — nothing in flight)

## Loop summary (loop #20, 2026-08-01)

**M0125-0046 is CLOSED** — commit `b2ff5e7e`, pushed to `origin/tpcds-fix2`.

The filed diagnosis ("MHJ walker disqualifies InExpr") was WRONG on the
planner side — the residual WHERE conjunct was never in `mh.Filters` at all;
it sat in the `*Filter` ABOVE the MHJ, which neither MHJ pass reads. Fix:
`pushResidualQualsIntoMHJTables` arm on `pushInnerJoinInputQuals`
(inner_join_qual_pushdown.go), attribution via fail-closed walkExprRefs +
cumulative offsets + name check, descent via pushConjunctIntoSubtree
(property-2 duplicate); mh.Filters-pass wrapper now stamps LeafLocal so both
passes compose. Executor half WAS real: `multi_hash_join.go::walkColumnRefs`
now mirrors the planner walker (literal IN admitted, 6 missing kinds added,
fail-closed default arm). Probe MHJ 96,562 → 11,049 rows = answer = oracle.

Gates all PASS: units precommit; planner/executor/parser suites (new walker
pinned `nonRecursiveClassifier` in exprwalk_inventory_test.go); spotcheck
Q12=2 Q13=35; SF0.5 subset probe (15 MHJ-heavy queries, FORCE=1, private
bin) PASS=15 MISMATCH=0 (Q72 straddled 300 s on the saturated host, PASS
solo at 600 s); plan-diff vs m0125-0044-after 5/22 DIFFER — ALL benign
(+Filter under MHJ member scans, zero structural change), rows proven:
Q3/Q10/Q11/Q21 = pinned anchors, Q2 md5-identical (455 rows) vs a HEAD
worktree baseline binary on the same data. New baseline captured:
`plan_snapshots/m0125-0046-after.txt` — diff the NEXT loop against THIS.

**Next loop: read the `## Current Priority` banner FIRST.** It now names
**`M0125-0038`** (no cost/cardinality propagation above base scans — the
LAST open M0125 task); after it, M0125 closes and M0126 (cost-driven
planning) is next per the 2026-07-31 USER amendment.

Notes: PG TPC-H reference (65432) load DIFFERS from the goopg load —
cross-cluster row-count comparison is INVALID for load-dependent queries
(control: Q3 11356 vs 11521). Ledger: arm-(b) row flipped resolved; 2 new
rows (duplicate-not-move; FuncCall/provolatile veto). Join-spine-descent-
into-MHJ row stays open. Servers: 65433 STOPPED deliberately so the running
nightly (20260801-011802) could clone the data dir (it was waiting);
sf05/PG-65432 restored to down; PG 65438 left UP as found. Nightly testport
FAIL = already-filed AI-20260731-001201-001, parked per banner.
In-flight: none.
