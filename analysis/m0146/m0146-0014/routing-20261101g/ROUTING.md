# M0146-0014 — census at 91a958dd2 (2026-11-01, loop #20): exhausted

Trigger: `scripts/s9-census-due.sh` returned DUE (base `e1f19ff73`, reason=plan-relevant-change)
with triggering path `internal/optimizer/planner.go` — the M0148-0001c slice (c) part 1 code
commit `91a958dd2` (multi-agg min/max tlist plans as N InitPlans over one childless Result).
The change is a gate widening, not a costing change: `rewriteMinMaxAggregates` now fires for a
tlist holding one-or-more strict min/max aggs (per-agg loop, `buildMinMaxInitPlan`), the
inheritance/partition declines and every non-min/max gate are untouched, and no renderer site
changed. Neither corporum carries a multi-target scalar min/max query — TPC-DS's bare `min`/`max`
selects keep the single-target shape and take the byte-identical pre-change path — so
`fires=none` is the expected outcome, and the capture measured exactly that.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines (only `.ralph/`, `docs/`, `analysis/`, `ci/logs/` bookkeeping was dirty). This is an
unchanged census under S9(4a), so only this file is committed; the captures are deleted from
`tmp/census-20261101g/` after this record.
code_tree=f36fd3a68f4fdafeb0734fb72356b0a3a500c3aac235e81fc8fdf873ba16088b (gate stamp,
dirty_code false); capture repo head `91a958dd2` (the code commit; this record lands on top).

Capture: `BASELINE_REV=e1f19ff73 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9f tmp/census-20261101g`
(BASELINE_REV = `e1f19ff73`, the commit consumed by routing-20261101f, so the plan A/B spans the
whole window since that census — `60d9013d4` the loop #17 baton and `91a958dd2` this loop's code
commit; fires=none on all three corpora, PLAN-PARITY 99/99 + 99/99 + 22/22 vs PG on the candidate
tree, and the A/B's own baseline-vs-candidate match was 99/99 + 99/99 + 22/22 with divergent=0;
the per-corpus fires files are empty and the fires diffs header-only.)

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

SF1's raw `CATEGORIES` line shows `rendering=1` on BOTH arms (baseline `e1f19ff73` and candidate
`91a958dd2`) — the pre-existing SF1-only rendering divergence, unchanged by this slice; the
match-counted channel (`CATEGORIES-EXCL-MATCH`) is 0 on both arms. Top-residual: unchanged —
`join-order=0` everywhere, the rendering=1 SF1 artifact is the only non-zero category.
