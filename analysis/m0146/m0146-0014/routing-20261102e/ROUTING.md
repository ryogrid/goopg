# M0146-0014 — census at 0e11ec1de (2026-11-02, run-2 loop #13): exhausted tree=d654c5b61b7a640ebe1612dabc8750077f8d8b75

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261102d, reason=plan-relevant-change)
— code commit `0e11ec1de` (M0148-0001c2 arm (2): relhassubclass parents skip the 10-page floor; fan-out
appendrels sized as the sum of their members). Neither corpus has an inheritance or a
partitioned table, so `fires=none` is expected and was measured. `git diff --quiet -- internal cmd
scripts bench go.mod go.sum` held while the capture built its engines, hence `tree=` above. Capture
repo head `56d1c3aa4` (the slice's records commit on top of the code commit).

Capture: `BASELINE_REV=896430e99 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9n tmp/census-20261102e`
(BASELINE_REV = the commit consumed by routing-20261102d; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9n-candidate`
sha256 `0f7886ec571bd4131d6674fa66f1bab93822087f20c6986a1b2a59fd3144c600`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261102e/flat` (exit 0):

- `tpch IDENTICAL new=ce932e73bc646988fbc215145f90119edbb233bdaf1884af4402442a6ba28421 base=routing-20261030c/s9w17-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 base=routing-20261030c/s9w17-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d base=routing-20261030c/s9w17-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261102d's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M0148 queue.
