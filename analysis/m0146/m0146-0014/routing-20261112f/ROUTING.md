# M0146-0014 — census at 21f23375c (2026-11-12, run-2 loop #20): exhausted tree=83313243d68b8bfa9a43606fce0223c62bcbd250

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261112e, reason=plan-relevant-change)
— code commit `21f23375c` (M0148-0001c3a2: bitmap ScalarArrayOp carrier + PG 18 OR-arm grouping;
predicate_implied_by_recurse port). The trigger files were `internal/optimizer/path.go`, `plan.go`,
`createplanbitmap.go`, `pathbitmap.go`, `pathbitmapor.go` and `internal/executor/operators_explain.go`. The census plan A/B measured
`fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `a478f112c`.

Capture: `BASELINE_REV=89b29765c CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9bq tmp/census-20261112f`
(BASELINE_REV = the head consumed by routing-20261112e; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9bq-candidate`
sha256 `2dd06afc42aedab550b94da7fd6317f936632f4dacd3e9f6cbf438ee6c30b7f4`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261112f/flat` (exit 0):

- `tpch IDENTICAL new=62dae4067986ea6ecd0e210ca537d70e9e58ec8574d63a3a935956adf62c52b4 base=routing-20261104e/s9bb-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 base=routing-20261104e/s9bb-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d base=routing-20261104e/s9bb-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261112e's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
