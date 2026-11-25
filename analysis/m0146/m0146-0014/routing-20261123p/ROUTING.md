# M0146-0014 — census at 7c6c60604 (2026-11-19): exhausted tree=2976ea35ecf63bb57bc54e156cca86c65e52d1e3

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261123o, reason=plan-relevant-change)
— code commit `7c6c60604` (M0149-0006 slice 2: orphan files).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `7c6c60604`.

Capture: `BASELINE_REV=2bd12fdbe CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ip tmp/census-20261123p`
(BASELINE_REV = the head consumed by routing-20261123o; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ip-candidate`
sha256 `cd28601c529aecb570d2cd1414e475a987ed5448a0c579908a1bfb7c887190c9`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261123p/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261123o's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
