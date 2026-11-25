# M0146-0014 — census at 9deacbc00 (2026-11-19): exhausted tree=4f6a8aea3041b9e46e2cd0c5ce3364685a154af2

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261124b, reason=plan-relevant-change)
— code commit `9deacbc00` (M0149-0015c: shutdown checkpoint streamed).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `9deacbc00`.

Capture: `BASELINE_REV=d0a79274a CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ic tmp/census-20261124c`
(BASELINE_REV = the head consumed by routing-20261124b; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ic-candidate`
sha256 `52c0c4f1b3a2cb4bee606e01ad927980141ff3b1e601062f2e50c1579989c32d`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261124c/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261124b's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
