# M0146-0014 — census at a509c5ac6 (2026-11-19): exhausted tree=0ddf23ff5c7452c5ad3b43e024223e9ce8b8c58e

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261125v, reason=plan-relevant-change)
— code commit `a509c5ac6` (after a509c5ac6 (M0149-0050e ALTER TYPE SET; 0050b binary arrays)).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `a509c5ac6`.

Capture: `BASELINE_REV=fff19ea2c CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9iw tmp/census-20261125w`
(BASELINE_REV = the head consumed by routing-20261125v; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9iw-candidate`
sha256 `9b10e88f5bf84e8228f338d02d1f249bc2565699f208dd6c7bfcc33f123d0ce5`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261125w/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261125v's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
