# M0146-0014 — census at a6eb6d406 (2026-11-19): exhausted tree=f3edbf82a14e812f0cb041bda2e159f2c2e056db

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261123i, reason=plan-relevant-change)
— code commit `a6eb6d406` (M0149-0001h1a85a: every cast target checked; int[N] bounds).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `a6eb6d406`.

Capture: `BASELINE_REV=d1ec83a0c CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ij tmp/census-20261123j`
(BASELINE_REV = the head consumed by routing-20261123i; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ij-candidate`
sha256 `5a6550426314bbf228e16c5ed32c8a56b079c44cd7ed8cae3632ca609050a445`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261123j/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261123i's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
