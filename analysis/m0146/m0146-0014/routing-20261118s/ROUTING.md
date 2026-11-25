# M0146-0014 — census at d25cdc982 (2026-11-18): exhausted tree=40b677bfa29e3e76a31612d4ac9cadfcb8dffb89

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261118r, reason=plan-relevant-change)
— code commit `d25cdc982` (M0149-0001h1a45: timestamp(p) rounding, infinity output, EXPLAIN timestamp literals).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `d25cdc982`.

Capture: `BASELINE_REV=6526601b6 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9hs tmp/census-20261118s`
(BASELINE_REV = the head consumed by routing-20261118r; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9hs-candidate`
sha256 `b991968114e3fb4fca9863fd88ebc4d3df84551406c4af95162adeb6a07a840e`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261118s/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261118r's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
