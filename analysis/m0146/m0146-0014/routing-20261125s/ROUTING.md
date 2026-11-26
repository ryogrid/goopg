# M0146-0014 — census at be969f7b7 (2026-11-19): exhausted tree=632a203e40788517fc82fe98cf8822644c17c999

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261125r, reason=plan-relevant-change)
— code commit `be969f7b7` (after be969f7b7 (M0149-0048a pgoutput generated-column rule)).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `be969f7b7`.

Capture: `BASELINE_REV=fff19ea2c CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9is tmp/census-20261125s`
(BASELINE_REV = the head consumed by routing-20261125r; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9is-candidate`
sha256 `106046ea1b299cb96b741929fdb8ba8452f1fefb7f88c0f9d094b60ad14172b0`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261125s/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261125r's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
