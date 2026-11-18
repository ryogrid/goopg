# M0146-0014 — census at 952552a54 (2026-11-18): exhausted tree=898af8d2dd20c893f67eb197f59f7cba551a404d

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261118s, reason=plan-relevant-change)
— code commit `952552a54` (M0149-0001h1a48a: time carrier helpers, infinity date output).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `952552a54`.

Capture: `BASELINE_REV=d25cdc982 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ht tmp/census-20261118t`
(BASELINE_REV = the head consumed by routing-20261118s; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ht-candidate`
sha256 `f49569be13ff7a457ca88fa4e1aaa203d56ea752b0ee7d770e7a5d479c9976d2`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261118t/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261118s's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
