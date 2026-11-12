# M0146-0014 — census at 537cf8603 (2026-11-12, run-2 loop #30): exhausted tree=6e7ef9d6f0e448851a978aaeb5b39c14843ae176

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261112o, reason=plan-relevant-change)
— code commit `537cf8603` (M0148-0001c3a3 slice 4: Index Only Scan Backward, descending merge join, right\_merge\_direction by equivalence class).
The trigger files include `internal/optimizer/createplanindex.go`, `pathindexonly.go`, `joinpathsmergeouter.go`, `pathkeys_useful.go` and `createplanjoin.go`. The census plan A/B measured
`fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `f47409914`.

Capture: `BASELINE_REV=e850227a6 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ca tmp/census-20261112p`
(BASELINE_REV = the head consumed by routing-20261112o; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ca-candidate`
sha256 `8bcfa304d201a7e54d446b731d2f2f059ec0121affef0363c670f02582ff451c`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261112p/flat` (exit 0):

- `tpch IDENTICAL new=62dae4067986ea6ecd0e210ca537d70e9e58ec8574d63a3a935956adf62c52b4 base=routing-20261104e/s9bb-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 base=routing-20261104e/s9bb-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d base=routing-20261104e/s9bb-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261112o's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
