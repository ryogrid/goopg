# M0146-0014 — census at 608929548 (2026-11-11, run-2 loop #7): exhausted tree=1e9653395dd0a31ffc7d80437a5d162158420d0a

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261111c, reason=plan-relevant-change)
— code commit `608929548` (M0148-0001j4 item 3a: field selection on composite SQL-function parameters;
`internal/optimizer/fieldselect.go`, `internal/catalog/catalog.go`, `internal/executor/plpgsql_runtime.go`). The script's trigger file
was `internal/optimizer/fieldselect.go`. The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `cffa4f7e7`.

Capture: `BASELINE_REV=2f0f6237e CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9bg tmp/census-20261111d`
(BASELINE_REV = the head consumed by routing-20261111c; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9bg-candidate`
sha256 `dfe00da87c88688695da0372f14f79124a59b823df7716e4ec9c9969b618862a`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261111d/flat` (exit 0):

- `tpch IDENTICAL new=62dae4067986ea6ecd0e210ca537d70e9e58ec8574d63a3a935956adf62c52b4 base=routing-20261104e/s9bb-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 base=routing-20261104e/s9bb-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d base=routing-20261104e/s9bb-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261111c's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
