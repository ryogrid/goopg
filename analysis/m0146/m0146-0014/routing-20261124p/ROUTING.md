# M0146-0014 — census at 94e24a6a6 (2026-11-19): exhausted tree=575e3c7db38db395a9a040ce40b85945058b8924

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261124o, reason=plan-relevant-change)
— code commit `94e24a6a6` (M0149-0077d: WAL pg_stat_io counters).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `94e24a6a6`.

Capture: `BASELINE_REV=a096ff734 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ip tmp/census-20261124p`
(BASELINE_REV = the head consumed by routing-20261124o; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ip-candidate`
sha256 `5ce99b5283ad885838cd57c8e3e18d502ce67d660d51ec486135a4e1a5799f37`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261124p/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261124o's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
