# M0146-0014 — census at 05b02362f (2026-11-04, run-2 loop #53): exhausted tree=2dd2795b908748f53e09ba2829552ccc69ec4172

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261104e, reason=plan-relevant-change)
— code commit `05b02362f` (M0148-0001j2: DROP TYPE / DROP DOMAIN reach columns, composite attributes
and domains; `internal/executor/drop_type_dependents.go`, `internal/executor/operators_ddl.go`,
`internal/catalog/catalog.go`). Its own fire-set gate measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `8c1531f51` (records on top of the
code commit).

Capture: `BASELINE_REV=406e379a4 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9bc tmp/census-20261104f`
(BASELINE_REV = the head consumed by routing-20261104e; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9bc-candidate`
sha256 `1c29954cf2859c5ac57dc843f41195a3acc1fd447531770c8e46cb2f887a0879`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261104f/flat` (exit 0):

- `tpch IDENTICAL new=62dae4067986ea6ecd0e210ca537d70e9e58ec8574d63a3a935956adf62c52b4 base=routing-20261104e/s9bb-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 base=routing-20261104e/s9bb-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d base=routing-20261104e/s9bb-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261104e's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
