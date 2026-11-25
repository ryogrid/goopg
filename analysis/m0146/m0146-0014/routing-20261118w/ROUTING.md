# M0146-0014 — census at f0c4cfc73 (2026-11-18): exhausted tree=1fed9f6891bfa03fb0312fd45a38c21afcd9541e

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261118v, reason=plan-relevant-change)
— code commit `f0c4cfc73` (M0149-0001h1a48e: PG's timestamp and date range).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `f0c4cfc73`.

Capture: `BASELINE_REV=96c5433f5 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9hw tmp/census-20261118w`
(BASELINE_REV = the head consumed by routing-20261118v; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9hw-candidate`
sha256 `07d02cbce6a477e543f728d1572a4db5f347972a6c6601b6ef2ad8b9dc901e7a`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261118w/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261118v's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
