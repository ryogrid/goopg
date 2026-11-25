# M0146-0014 — census at a096ff734 (2026-11-19): exhausted tree=a404f52f882839444e7fd28e80bad63eaed5fa6b

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261124n, reason=plan-relevant-change)
— code commit `a096ff734` (M0149-0077b/c: standby DML guard, SHOW port).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `a096ff734`.

Capture: `BASELINE_REV=68084c3ed CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9io tmp/census-20261124o`
(BASELINE_REV = the head consumed by routing-20261124n; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9io-candidate`
sha256 `4c9fd18e192bcaada05ee177d1fbf4d7612b6b999b359c52ed44b602260aa5c1`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261124o/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261124n's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
