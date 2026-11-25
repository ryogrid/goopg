# M0146-0014 — census at 6f43598e3 (2026-11-19): exhausted tree=2e007e111d0e2e37f7bd053397d66ecff2ad3f4c

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261119h, reason=plan-relevant-change)
— code commit `6f43598e3` (M0149-0001h1a59: operators on time and timetz).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `6f43598e3`.

Capture: `BASELINE_REV=e7ed10d48 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ii tmp/census-20261119i`
(BASELINE_REV = the head consumed by routing-20261119h; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ii-candidate`
sha256 `3694895f41a3656e317f4dc4380df33f1e8b8cceab7099490e9b7538b0279955`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261119i/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261119h's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
