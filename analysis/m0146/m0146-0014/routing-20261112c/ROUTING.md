# M0146-0014 — census at 8cc9a4e51 (2026-11-12, run-2 loop #16): exhausted tree=680b491a8c879b2767da308a53a96a6e86e7817f

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261112b, reason=plan-relevant-change)
— code commit `8cc9a4e51` (M0148-0001c3a1: a literal coerced to name prints as 'x'::name;
`internal/executor/operators_explain.go`). The script's trigger file
was `internal/executor/operators_explain.go`. The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `079d14643`.

Capture: `BASELINE_REV=a56b50e03 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9bn tmp/census-20261112c`
(BASELINE_REV = the head consumed by routing-20261112b; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9bn-candidate`
sha256 `03c13923a66be7bf4c62784bf0c5d5db9501a759ad9b580719e0985866c472eb`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261112c/flat` (exit 0):

- `tpch IDENTICAL new=62dae4067986ea6ecd0e210ca537d70e9e58ec8574d63a3a935956adf62c52b4 base=routing-20261104e/s9bb-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 base=routing-20261104e/s9bb-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d base=routing-20261104e/s9bb-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261112b's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
