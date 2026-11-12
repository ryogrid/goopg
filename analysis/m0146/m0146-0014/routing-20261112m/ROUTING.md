# M0146-0014 — census at e527d5ad2 (2026-11-12, run-2 loop #27): exhausted tree=490cbf061e84e9e3c999b4dc97e2a1502331fa46

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261112l, reason=plan-relevant-change)
— code commit `e527d5ad2` (M0148-0001c3a3: nbtree ReverseScanCursor, no caller yet).
The trigger files were `internal/access/nbtree/reverse_scan*.go`. The census plan A/B measured
`fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `dd744c89d`.

Capture: `BASELINE_REV=1d8f4598b CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9bx tmp/census-20261112m`
(BASELINE_REV = the head consumed by routing-20261112l; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9bx-candidate`
sha256 `5b3ee98924e29aef26b43c6e65e359c2b3855b084f866a3f41e821200f25e60d`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261112m/flat` (exit 0):

- `tpch IDENTICAL new=62dae4067986ea6ecd0e210ca537d70e9e58ec8574d63a3a935956adf62c52b4 base=routing-20261104e/s9bb-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 base=routing-20261104e/s9bb-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d base=routing-20261104e/s9bb-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261112l's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
