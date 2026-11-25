# M0146-0014 — census at 96d734112 (2026-11-11, run-2 loop #13): exhausted tree=b0b7048adfadf0eae468993c996b67cc0eb6e496

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261111g, reason=plan-relevant-change)
— code commit `96d734112` (M0148-0001c3a slice 3: a NullTest behind an equality prefix is an index qual;
`internal/optimizer/pathindexrestrict.go`, `pathindexonly.go`, `createplanindex.go`, `internal/executor/operators_explain.go`). The script's trigger file
was `internal/optimizer/pathindexrestrict.go`. The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `edf954c49`.

Capture: `BASELINE_REV=2cc8f8598 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9bk tmp/census-20261111h`
(BASELINE_REV = the head consumed by routing-20261111g; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9bk-candidate`
sha256 `aa5163f3e60bd65fecd8c95f73c0e5442a2907f543b1b7e3c3bfa3984eefa3d1`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261111h/flat` (exit 0):

- `tpch IDENTICAL new=62dae4067986ea6ecd0e210ca537d70e9e58ec8574d63a3a935956adf62c52b4 base=routing-20261104e/s9bb-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 base=routing-20261104e/s9bb-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d base=routing-20261104e/s9bb-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261111g's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
