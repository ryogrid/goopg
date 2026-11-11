# M0146-0014 — census at 88ef78511 (2026-11-11, run-2 loop #2): exhausted tree=745d7b239f1d466bb9961a0864ef50b07b919326

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261104f, reason=plan-relevant-change)
— commit `88ef78511` (titled as a documentation commit) also carried code: the M0148-0001j3
type USAGE privilege checks (`internal/executor/type_usage_acl.go`, `internal/executor/operators_ddl.go`,
`internal/catalog/catalog.go` HoldsGrantOption) and the ROLLBACK role revert
(`internal/postmaster/conn_tx.go`, `query.go`, `txn_verb.go`). The script's trigger file was
`internal/catalog/catalog.go`. The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `88ef78511`.

Capture: `BASELINE_REV=8c1531f51 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9bd tmp/census-20261111a`
(BASELINE_REV = the head consumed by routing-20261104f; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9bd-candidate`
sha256 `c2c166eaadf29344ddfcc22442d99b2c37b9bf13bf72b6cd44a85d8bc9ac860a`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261111a/flat` (exit 0):

- `tpch IDENTICAL new=62dae4067986ea6ecd0e210ca537d70e9e58ec8574d63a3a935956adf62c52b4 base=routing-20261104e/s9bb-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 base=routing-20261104e/s9bb-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d base=routing-20261104e/s9bb-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261104f's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
