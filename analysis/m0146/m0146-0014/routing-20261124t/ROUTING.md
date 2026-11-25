# M0146-0014 — census at d3b1c3a9c (2026-11-19): exhausted tree=c039755f6c2e007e233b63c3583c62af09a6e789

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261124s, reason=plan-relevant-change)
— code commit `d3b1c3a9c` (backup functions, slot advance, BASE_BACKUP progress (M0149-0077j/k/l)).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `d3b1c3a9c`.

Capture: `BASELINE_REV=668e5bf0b CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9it tmp/census-20261124t`
(BASELINE_REV = the head consumed by routing-20261124s; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9it-candidate`
sha256 `fa4901a364ff24b0323c333540da62c1a7fa096b9d57255cad1f5597ecc3ebde`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261124t/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261124s's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
