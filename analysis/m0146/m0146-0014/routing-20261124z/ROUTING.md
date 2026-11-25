# M0146-0014 — census at 81b57146b (2026-11-19): exhausted tree=03943a469e997c776ea14c64100c8232879b7812

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261124y, reason=plan-relevant-change)
— code commit `81b57146b` (recovery targets by name, time and immediate (M0149-0023a)).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `81b57146b`.

Capture: `BASELINE_REV=46061eac1 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9iz tmp/census-20261124z`
(BASELINE_REV = the head consumed by routing-20261124y; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9iz-candidate`
sha256 `5e5fa47e72b3ff0af493bb18dc0eee504fada491ae305c9faba1070c1b16de18`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261124z/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261124y's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
