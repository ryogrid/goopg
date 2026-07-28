(idle — nothing in flight)

Last loop (#15, 2026-07-29): **M0124-0001 CLOSED.** The merged D7 deliverable
`analysis/tpcds-sf1-goopg-20260728.md` landed. The sweep was NOT re-run — every
figure is read back from `analysis/tpcds-sf1-resweep-20260728/`. Result: 11 of
13 §13.3 projections confirmed as stated, 2 (Q50, Q46) confirmed on rows and
REFUTED on values, 0 refuted outright; the projected **21** goopg-only defects
measure **40** (ERROR 2 + TIMEOUT 17 [15 unbounded / 2 budget-marginal Q18+Q35]
+ wrong-row-count 3 + **wrong-answer-behind-a-matching-row-count 18**).
**The engine-commit freeze is LIFTED.**

NEXT LOOP — banner still M0124 → M0125 (M-NIGHTLY PARKED: keep FILING `## AI-`
items, do not select; `ci/logs/action-items.md` unchanged since 2026-07-25, all
26 already filed as ID RANGES `-008..-026`, so a per-ID grep FALSE-NEGATIVES —
grep loosely, e.g. `grep 20260725 .ralph/fix_plan.md`).

**Recommended: M0125-0009** — the first engine fix now that the freeze is
lifted. One-line root cause (`parserExprKey`'s `%T` fallback collapses sibling
`sum(CASE …)` targets), 10 queries of evidence (Q2 Q21 Q40 Q43 Q50 Q59 Q62 Q66
Q97 Q99), and the most legible instance in the sweep is Q97 —
`store_only|catalog_only|store_and_catalog` = `392155|392155|392155` against PG's
`541140|286927|161`, three disjoint sets, so equal cardinalities are impossible.
Flat reproducer, no subquery: `select sum(case…), sum(case…) from date_dim`
→ `10435|10435`.

**M0125-0010 is a close second and INDEPENDENT** — `remapSubqueryColumnRefs`
(`internal/planner/planner.go:2450`, name-match + `break` at `:2468`) binds
FROM-subquery `Project` targets by column name, and an `Aggregate` names outputs
after the function. 4 queries (Q28 Q46 Q68 Q79). Reproducer uses no `CASE`:
`select * from (select sum(d_dom), sum(d_year) from date_dim) d`
→ `1149021|1149021` vs PG `1149021|146061700`. Neither defect subsumes the other.

Acceptance for BOTH: value-compare, never row counts —
`scripts/tpcds-value-diff.py bench/tpcds/runtime_goopg/tpcds-results <q>` (D6a).
Never score a Q18/Q35 verdict flip or a Q50/Q46 row-count match as a win.
Planner commits also need the TPC-H spotcheck + a timed 22-query power run.

Gates run: `make ralph-state-guard` PASS; pre-commit hook (pgbench smoke) PASS.
In-flight: none.
