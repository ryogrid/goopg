# M0146-0014 — census at 41be0e3ca (2026-11-03, run-2 loop #45): exhausted tree=f231c23e3cda49fca1653551719c9b586015dea9

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261103g, reason=plan-relevant-change)
— code commit `41be0e3ca` (M0148-0001j1: grouping sets with an ordered aggregate run the sorted
rollups; DROP TYPE dependents; `internal/optimizer/groupingpaths.go`,
`internal/optimizer/partialaggupper.go`, `internal/executor/drop_type_dependents.go`,
`internal/executor/operators_ddl.go`). Its own fire-set gate measured `fires=none` on all three
corpora. `git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built
its engines, hence `tree=` above (HEAD's tree). Capture repo head `43eda7b58` (records on top of the
code commit).

Capture: `BASELINE_REV=1aac37760 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ao tmp/census-20261103h`
(BASELINE_REV = the head consumed by routing-20261103g; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ao-candidate`
sha256 `3f65d4b0017b10d1ea37a5943c6741f3e8a11ea93b57b2f6d5bcc7738dc0020c`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261103h/flat` (exit 0):

- `tpch IDENTICAL new=5175c597d5c008062df3cb56dc0f007e5eb92d2bb2019caf9d988cbb384c0f36 base=routing-20261102f/s9o-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 base=routing-20261102f/s9o-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d base=routing-20261102f/s9o-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261102f's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
