# M0146-0014 — census at be6c4b1d2 (2026-11-19): exhausted tree=e97764f80b466565d1a992e474783345ae00b9f7

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261125f, reason=plan-relevant-change)
— code commit `be6c4b1d2` (standby schema refresh (M0149-0032d)).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `be6c4b1d2`.

Capture: `BASELINE_REV=fff19ea2c CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ig tmp/census-20261125g`
(BASELINE_REV = the head consumed by routing-20261125f; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ig-candidate`
sha256 `0bc897ede97cf47ebe4e05da2f246454d1f1e226a0fad33e1da9b3c2e25d86ad`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261125g/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261125f's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
