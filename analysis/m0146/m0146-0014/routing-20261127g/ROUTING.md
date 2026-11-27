# M0146-0014 — census at 80586d4a2 (2026-11-19): exhausted tree=4f4424fb3cc4a6726ee906b81903f8d107f063be

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261127f, reason=plan-relevant-change)
— code commit `80586d4a2` (031 column lists ported).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `80586d4a2`.

Capture: `BASELINE_REV=fff19ea2c CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ig tmp/census-20261127g`
(BASELINE_REV = the head consumed by routing-20261127f; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ig-candidate`
sha256 `9e8c25747f5d20b224cd41c0d8c9d5134b410ce2150a1023db1baad570e7e693`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261127g/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261127f's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
