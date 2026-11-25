# M0146-0014 — census at 0617a1952 (2026-11-12, run-2 loop #22): exhausted tree=84d5d2f8173b141e1d21168b26a5a95fa5dce2c0

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261112g, reason=plan-relevant-change)
— code commit `0617a1952` (M0148-0001c3a2: choose_bitmap_and port; BitmapAnd costing on baserel
tuples, per-row charge on path rows). The trigger files were `internal/optimizer/costbitmap.go`,
`pathbitmap.go`, `pathbitmapor.go` and `createplanbitmap.go`. The census plan A/B measured
`fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `81fdf350a`.

Capture: `BASELINE_REV=c8b14ed30 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9bs tmp/census-20261112h`
(BASELINE_REV = the head consumed by routing-20261112g; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9bs-candidate`
sha256 `5d27630e1da00e5d0c35069f5f56e7f2fde5222df3989d4d3a2d930c92d59fc2`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261112h/flat` (exit 0):

- `tpch IDENTICAL new=62dae4067986ea6ecd0e210ca537d70e9e58ec8574d63a3a935956adf62c52b4 base=routing-20261104e/s9bb-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 base=routing-20261104e/s9bb-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d base=routing-20261104e/s9bb-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261112g's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
