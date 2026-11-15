# M0146-0014 — census at 2ab2cbe4c (2026-11-15): exhausted tree=25ca5cc39572d87ea3e3d631459ffad5250cec57

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261115q, reason=plan-relevant-change)
— code commit `2ab2cbe4c` (M0149-0001a: initdb writes template1's finished catalog image to base/1).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `2ab2cbe4c`.

Capture: `BASELINE_REV=900c5bbe9 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9dv tmp/census-20261115r`
(BASELINE_REV = the head consumed by routing-20261115q; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9dv-candidate`
sha256 `10f662b5a09b20645d1eb0c0660dded4b46ebbc4ecdce4d001f78a456ae2ede2`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261115r/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261115q's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
