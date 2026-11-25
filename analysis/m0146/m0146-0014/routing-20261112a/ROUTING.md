# M0146-0014 — census at 49bbd0613 (2026-11-12, run-2 loop #14): exhausted tree=275bddb6dbb87ec3fcc695b9e41a8c6ef6ae3767

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261111h, reason=plan-relevant-change)
— code commit `49bbd0613` (M0148-0001c3a1: bitmap index scans carry the NullTest index qual;
`internal/optimizer/pathbitmap.go`, `createplanbitmap.go`, `plan.go`, `internal/executor/operators_bitmap.go`). The script's trigger file
was `internal/optimizer/pathbitmap.go`. The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `8ac73493e`.

Capture: `BASELINE_REV=edf954c49 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9bl tmp/census-20261112a`
(BASELINE_REV = the head consumed by routing-20261111h; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9bl-candidate`
sha256 `8372205c6322567a3dff31deb5e2fa64f46b989c63f412ef4a6906cdbb5cd00e`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261112a/flat` (exit 0):

- `tpch IDENTICAL new=62dae4067986ea6ecd0e210ca537d70e9e58ec8574d63a3a935956adf62c52b4 base=routing-20261104e/s9bb-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 base=routing-20261104e/s9bb-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d base=routing-20261104e/s9bb-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261111h's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
