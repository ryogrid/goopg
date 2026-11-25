# M0146-0014 — census at 89031bbc1 (2026-11-11, run-2 loop #11): exhausted tree=d2f7d5fa46deb6757e8b32ea3bdb4c5e779ed2e2

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261111e, reason=plan-relevant-change)
— code commit `89031bbc1` (M0148-0001c3a slice 1: `col IS [NOT] NULL` on the leading key column is a btree index qual;
`internal/optimizer/pathindexrestrict.go`, `createplanindex.go`, `plan.go`, `internal/executor/operators_index.go`). The script's trigger file
was `internal/optimizer/pathindexrestrict.go`. The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `3218fd9eb`.

Capture: `BASELINE_REV=2a21c4185 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9bi tmp/census-20261111f`
(BASELINE_REV = the head consumed by routing-20261111e; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9bi-candidate`
sha256 `32d37eef1dca35e9f79d7ed473552c79ec8d47032fd60d87fa7525abdfef0a47`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261111f/flat` (exit 0):

- `tpch IDENTICAL new=62dae4067986ea6ecd0e210ca537d70e9e58ec8574d63a3a935956adf62c52b4 base=routing-20261104e/s9bb-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 base=routing-20261104e/s9bb-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d base=routing-20261104e/s9bb-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261111e's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
