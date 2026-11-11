# M0146-0014 — census at c8807e4c1 (2026-11-12, run-2 loop #21): exhausted tree=ef8487325a483017365c9871a84c650d8de811f2

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261112f, reason=plan-relevant-change)
— code commit `c8807e4c1` (M0148-0001c3a2: plain-path OR->SAOP; AND/OR NULL-arm fold fix;
find_duplicate_ors qual reduction). The trigger files were `internal/optimizer/foldconst.go`,
`pathindexrestrict.go` and `pathbitmapor.go`. The census plan A/B measured
`fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `c8b14ed30`.

Capture: `BASELINE_REV=a478f112c CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9br tmp/census-20261112g`
(BASELINE_REV = the head consumed by routing-20261112f; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9br-candidate`
sha256 `951a8c48378f876bdc6764c7481014fbf4d14316ca3a9080fe9b1b53063f2991`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261112g/flat` (exit 0):

- `tpch IDENTICAL new=62dae4067986ea6ecd0e210ca537d70e9e58ec8574d63a3a935956adf62c52b4 base=routing-20261104e/s9bb-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 base=routing-20261104e/s9bb-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d base=routing-20261104e/s9bb-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261112f's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
