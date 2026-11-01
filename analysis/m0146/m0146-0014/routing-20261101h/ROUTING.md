# M0146-0014 — census at c4b80c6d6 (2026-11-01, run-2 loop #3): exhausted tree=f370ea06167b4d60c5ff30ad9a1c22256cec68a8

Trigger: `scripts/s9-census-due.sh` returned DUE (base `91a958dd2`, reason=plan-relevant-change)
— the M0148-0001c slice (c2) code commit `c4b80c6d6` (inheritance-parent min/max plans
`Limit → Merge Append` InitPlans; `SetOp.RTID`/`AppendRelName` naming; persistent
`explainNames.labelSeen`; DESC-index min/max direction fix). Neither corpus carries an
inheritance parent, a DESC index, or a min/max over one, so `fires=none` is the expected outcome,
and the capture measured exactly that. `git diff --quiet -- internal cmd scripts bench go.mod
go.sum` held while the capture built its engines (only `.ralph/`, `docs/`, `analysis/`,
`ci/logs/` bookkeeping was dirty), hence `tree=` above (`git write-tree` at capture time).
Capture repo head `9b21cb7c2` (the slice's records commit on top of the code commit).

Capture: `BASELINE_REV=91a958dd2 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9h tmp/census-20261101h`
(BASELINE_REV = the commit consumed by routing-20261101g; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9h-candidate`
sha256 `96496f86389075a9d610fa1eb49f8391fe4339881f1cd3d2076cad148e5f93ae`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261101h/flat` (exit 0):

- `tpch IDENTICAL new=ce932e73bc646988fbc215145f90119edbb233bdaf1884af4402442a6ba28421 base=routing-20261030c/s9w17-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 base=routing-20261030c/s9w17-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d base=routing-20261030c/s9w17-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261101g's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M0148-0001c1 (queue head).
