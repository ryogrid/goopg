# M0146-0014 — census at 300dd9ee9 (2026-11-14): exhausted tree=754b3e854807d54890b4594cb3edc393357880e3

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261114c, reason=plan-relevant-change)
— code commit `300dd9ee9` (M0148-0001c3a4 item (i) slice 4: the plain Index Scan trailing array behind a leading range).
The trigger files include `internal/optimizer/pathindexonly.go`, `pathindexrestrict.go`, `createplanindex.go` and
`internal/executor/operators_explain.go`. The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `592657b89`.

Capture: `BASELINE_REV=f92e0006b CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ci tmp/census-20261114d`
(BASELINE_REV = the head consumed by routing-20261114c; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ci-candidate`
sha256 `b01f15888c85508e9011fe0a15104e1ac60a8a73c49d412c3d8e2f33e7238239`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261114d/flat` (exit 0):

- `tpch IDENTICAL new=62dae4067986ea6ecd0e210ca537d70e9e58ec8574d63a3a935956adf62c52b4 base=routing-20261104e/s9bb-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 base=routing-20261104e/s9bb-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d base=routing-20261104e/s9bb-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261114c's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
