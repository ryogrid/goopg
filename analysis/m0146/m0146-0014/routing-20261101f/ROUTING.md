# M0146-0014 — census at e1f19ff73 (2026-11-01, loop #17): exhausted

Trigger: `scripts/s9-census-due.sh` returned DUE (base `31b75104bd7a`, reason=plan-relevant-change)
with triggering paths `internal/optimizer/planner.go` and `internal/executor/operators_explain.go` —
the M0148-0001c slice (b) commit `e1f19ff73` (multi-target min/max tlist plans as
`Sort → ProjectSet → childless Result`). The change is a gate widening, not a costing change:
`rewriteMinMaxAggregates` now fires for a tlist holding exactly one strict min/max agg plus
jointree-surviving extras (constants / bare `generate_series` over constants), and the only
renderer addition is a `*optimizer.ProjectSet` arm in the EXPLAIN key chase, which runs when a
plan is PRINTED. Neither corporum carries a multi-target scalar min/max query — TPC-DS's bare
`min`/`max` selects keep the single-target shape and take the byte-identical pre-change path —
so `fires=none` is the expected outcome, and the capture measured exactly that.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines (the code commit is in HEAD; only `.ralph/`, `docs/`, `analysis/`, `ci/logs/` bookkeeping
was dirty). This is an unchanged census under S9(4a), so only this file is committed; the captures
are deleted from `tmp/census-20261101f/` after this record.
code_tree=1f1ba3af18a326ca651323fbdb2e318d37148a0f841f95cb2e1353edcb2f2cb8 (gate stamp, dirty_code
false); capture repo head `e1f19ff73` (the code commit; the recording commit is this file on top,
with the loop's record commit `d36d05876` in between).

Capture: `BASELINE_REV=31b75104b CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9f tmp/census-20261101f`
(BASELINE_REV = `31b75104b`, the commit consumed by routing-20261101e, so the plan A/B spans the
whole window since that census — 31b75104b..e1f19ff73: two record-only commits (`9c7b3bd61` the
loop #16 records, `047b86d8d` the loop #16 census, `6944b023a` the baton, `d36d05876` this loop's
records) plus this code commit; fires=none on all three corpora, PLAN-PARITY 99/99 + 99/99 + 22/22
vs PG on the candidate tree, and the A/B's own baseline-vs-candidate match was 99/99 + 99/99 +
22/22 with divergent=0; the per-corpus fires files are empty and the fires diffs header-only.)

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

SF1's raw `CATEGORIES` line shows `rendering=1` on BOTH arms (baseline `31b75104b` and candidate
`e1f19ff73`) — the pre-existing SF1-only rendering divergence, unchanged by this slice; the
match-counted channel (`CATEGORIES-EXCL-MATCH`) is 0 on both arms.
