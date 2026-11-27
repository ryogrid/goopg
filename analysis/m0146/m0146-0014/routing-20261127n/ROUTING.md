# M0146-0014 — census at 1c3a23822 (2026-11-19): exhausted tree=6cad40eecb4a99dc004dbd87094d4629938032a2

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261127m, reason=plan-relevant-change)
— code commit `1c3a23822` (after 1c3a23822 (M0149-0063b apply stream spool)).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `1c3a23822`.

Capture: `BASELINE_REV=fff19ea2c CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9in tmp/census-20261127n`
(BASELINE_REV = the head consumed by routing-20261127m; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9in-candidate`
sha256 `6ef790dcef5b96d37c9112cde5a363b227fba76656f8ce56181f3318b6d03b54`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261127n/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261127m's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
