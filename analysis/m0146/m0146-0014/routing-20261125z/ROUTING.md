# M0146-0014 — census at 7200f4a5e (2026-11-19): exhausted tree=e9fd3f44ce530bef76bc90514389b185f930eefc

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261125y, reason=plan-relevant-change)
— code commit `7200f4a5e` (M0149-0052a walsender pid + missing-publication warning).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `7200f4a5e`.

Capture: `BASELINE_REV=fff19ea2c CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9iz tmp/census-20261125z`
(BASELINE_REV = the head consumed by routing-20261125y; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9iz-candidate`
sha256 `0fdb2e7a68892351721df416cc17e4561d153eb75b72234a39c8cbf5fe1511d6`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261125z/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261125y's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
