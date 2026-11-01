# M0146-0014 — census at 896430e99 (2026-11-02, run-2 loop #12): exhausted tree=3d42e9742c79d691a5e2c02a9512e5bd58ee44b2

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261102c, reason=plan-relevant-change)
— code commit `896430e99` (M0148-0001d slice 2: a multi-leaf partition fan-out's top SetOp carries
the parent RTE, so Append-level columns name the partitioned parent). Neither corpus has a
partitioned table, so `fires=none` is expected and was measured. `git diff --quiet -- internal cmd
scripts bench go.mod go.sum` held while the capture built its engines, hence `tree=` above. Capture
repo head `b438dd6ce` (the slice's records commit on top of the code commit).

Capture: `BASELINE_REV=5ae6f5416 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9m tmp/census-20261102d`
(BASELINE_REV = the commit consumed by routing-20261102c; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9m-candidate`
sha256 `a6882dfcd6c88b066bcd0f51caa4eb564804a534a239ca444e061091aa901584`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261102d/flat` (exit 0):

- `tpch IDENTICAL new=ce932e73bc646988fbc215145f90119edbb233bdaf1884af4402442a6ba28421 base=routing-20261030c/s9w17-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 base=routing-20261030c/s9w17-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d base=routing-20261030c/s9w17-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261102c's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M0148 queue.
