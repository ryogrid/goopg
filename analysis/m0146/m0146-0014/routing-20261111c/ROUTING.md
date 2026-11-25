# M0146-0014 — census at 9de6fc9a8 (2026-11-11, run-2 loop #5): exhausted tree=2c88368be59f0382f7e4de7a22d78f2b08435138

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261111b, reason=plan-relevant-change)
— code commit `9de6fc9a8` (M0148-0001j4 item 2: pg_proc proargtypes/prorettype carry user type OIDs;
`internal/executor/sys_pg_proc.go`, `internal/initdb/pg_proc_view.go`). The script's trigger file
was `internal/initdb/pg_proc_view.go`. The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `a4e5861ca`.

Capture: `BASELINE_REV=581d1a818 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9bf tmp/census-20261111c`
(BASELINE_REV = the head consumed by routing-20261111b; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9bf-candidate`
sha256 `1a890e9bcb62c48f3e29f2d231cf44fca3925359b55922eee2603a270b24da64`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261111c/flat` (exit 0):

- `tpch IDENTICAL new=62dae4067986ea6ecd0e210ca537d70e9e58ec8574d63a3a935956adf62c52b4 base=routing-20261104e/s9bb-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 base=routing-20261104e/s9bb-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d base=routing-20261104e/s9bb-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261111b's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
