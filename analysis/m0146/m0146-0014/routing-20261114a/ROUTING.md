# M0146-0014 — census at a7c0f874b (2026-11-14): exhausted tree=b3105b3f79ff11548694a4bba6f2dbfa14373c06

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261112t, reason=plan-relevant-change)
— code commit `a7c0f874b` (M0148-0001c3a4 item (i) slice 1: quals on a ScalarArrayOp's own column
merge into the probe; array literals split into elements). The trigger files include
`internal/optimizer/pathbitmapor.go`, `pathindexrestrict.go`, `createplanindex.go` and
`internal/executor/operators_explain.go`. The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `4480b6790`.

Capture: `BASELINE_REV=bd0b8d3a8 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9cf tmp/census-20261114a`
(BASELINE_REV = the head consumed by routing-20261112t; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9cf-candidate`
sha256 `bf75c683149487729d650025a57dfb9533b400d05accd8318cbcf12c867586e3`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261114a/flat` (exit 0):

- `tpch IDENTICAL new=62dae4067986ea6ecd0e210ca537d70e9e58ec8574d63a3a935956adf62c52b4 base=routing-20261104e/s9bb-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 base=routing-20261104e/s9bb-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d base=routing-20261104e/s9bb-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261112t's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
