# M0146-0014 — census at d7cd30ef7 (2026-11-19): exhausted tree=2d39b4f00308ef1727e038f8dad7a7b1d971d848

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261119l, reason=plan-relevant-change)
— code commit `d7cd30ef7` (M0149-0001h1a65: number times interval and date/time literal operands).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `d7cd30ef7`.

Capture: `BASELINE_REV=e41129288 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9im tmp/census-20261119m`
(BASELINE_REV = the head consumed by routing-20261119l; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9im-candidate`
sha256 `c724cae4ac0774cd5ce3579c7070eb9687c27cc5585791e6d4697ada9089cdf8`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261119m/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261119l's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
