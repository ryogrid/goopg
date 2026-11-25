# M0146-0014 — census at e41129288 (2026-11-19): exhausted tree=a0332424e57bfda808bea8f493f1be89b6c36ac8

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261119k, reason=plan-relevant-change)
— code commit `e41129288` (M0149-0001h1a64: pg_typeof keeps a nested set-returning argument).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `e41129288`.

Capture: `BASELINE_REV=5f893dea0 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9il tmp/census-20261119l`
(BASELINE_REV = the head consumed by routing-20261119k; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9il-candidate`
sha256 `5e177818cd8bcc5427d94682807c6b1b9dfc1325c1d42fc98b08d77f73788769`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261119l/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261119k's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
