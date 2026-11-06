# M0146-0014 — census at 31b75104b (2026-11-01, loop #16): exhausted

Trigger: `scripts/s9-census-due.sh` returned DUE (base `0ad18e437738`, reason=plan-relevant-change)
with triggering path `internal/executor/operators_explain.go` — the M0148-0001c slice (a) commit
`31b75104b` (EXPLAIN Sort/Group Key over a min/max-rewritten Result renders `(InitPlan N).col1`).
The change is EXPLAIN-rendering only: the key chase (`resolveKeySourceDepth` Result arm +
`chaseKeyExprToInitPlanRef` + the `sortKeyParts` composite arm) runs when a plan is PRINTED, after
planning and independently of it — no planner cost, pathkey, or gate code is touched, so no query
can re-plan differently. `git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while
the capture built its engines (the code commit is in HEAD; only `.ralph/` bookkeeping was dirty).
This is an unchanged census under S9(4a), so only this file is committed; the captures are deleted
from `tmp/census-20261101e/` after this record.
tree=561211ba48a57f56f3fc2c2b63f2a7d202eb0e0828027f9da5a868485d6eba1d; capture repo head 31b75104b
(the code commit; the recording commit is this file on top).

Capture: `BASELINE_REV=0ad18e4377 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9f tmp/census-20261101e`
(BASELINE_REV = `0ad18e437`, the commit consumed by routing-20261101d, so the plan A/B spans the
whole window since that census — 0ad18e437..31b75104b: two record-only commits (`94cd3a8a7` the
loop #15 census record, `bd65d86bc` the baton) plus this code commit; fires=none on all three
corpora, PLAN-PARITY 22/22 + 99/99 + 99/99 vs PG on the candidate tree, and the A/B's own
baseline-vs-candidate match was 22/22 + 99/99 + 99/99 with divergent=0; the per-corpus
fires files are empty and the fires diffs header-only).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-H | 22 / 22 | all 0 |
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
