# M0146-0014 — census at eeb5120da (2026-11-19): exhausted tree=3266799fec7dab1800a55b9ad8640607baea9029

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261123r, reason=plan-relevant-change)
— code commit `eeb5120da` (M0149-0007 slice 2: checkpointer/WAL stats).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `eeb5120da`.

Capture: `BASELINE_REV=b284bc0af CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9is tmp/census-20261123s`
(BASELINE_REV = the head consumed by routing-20261123r; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9is-candidate`
sha256 `ed3a3b6ae6bd678a84cdd9c109c1c9741249ddda5146e8fb768f199fb42727dc`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261123s/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261123r's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
