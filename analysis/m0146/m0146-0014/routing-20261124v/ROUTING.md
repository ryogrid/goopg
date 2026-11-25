# M0146-0014 — census at c0acd7d43 (2026-11-19): exhausted tree=68e7694db567a06a47e9fb5954bbfc5f1ba84144

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261124u, reason=plan-relevant-change)
— code commit `c0acd7d43` (WAL archiver (M0149-0018a)).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `c0acd7d43`.

Capture: `BASELINE_REV=80c421684 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9iv tmp/census-20261124v`
(BASELINE_REV = the head consumed by routing-20261124u; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9iv-candidate`
sha256 `661163d35bc5694f7f23570bf3389b2c5df49e61265b1f1f8f5c57f53df8ac0c`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261124v/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261124u's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
