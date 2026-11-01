# M0146-0014 — census at 91b5b6308 (2026-11-02, run-2 loop #15): exhausted tree=f14098f0585db4e310478b2a77a439f83d492002

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261102f, reason=plan-relevant-change)
— code commit `91b5b6308` (M0148-0001c2 arm (3): the remaining min/max InitPlan branches —
composite-prefix IOS, max(const) Seq Scan under a cost-copying gating Result, equality-pinned Seq
Scan — priced as PG). No corpus plan carries those branches or a gating Result over a child, so
`fires=none` is expected and was measured. `git diff --quiet -- internal cmd scripts bench go.mod
go.sum` held while the capture built its engines, hence `tree=` above. Capture repo head
`ffdd24e23` (records commit on top of the code commit and its pins).

Capture: `BASELINE_REV=5040c8c72 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9p tmp/census-20261102g`
(BASELINE_REV = the commit consumed by routing-20261102f; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9p-candidate`
sha256 `57f7225b2d5266bee6705f908ee0be69658ef5c0927dcdb6d88f13f4dbb72cf3`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261102g/flat` (exit 0):

- `tpch IDENTICAL new=5175c597d5c008062df3cb56dc0f007e5eb92d2bb2019caf9d988cbb384c0f36 base=routing-20261102f/s9o-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 base=routing-20261102f/s9o-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d base=routing-20261102f/s9o-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261102f's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M0148 queue.
