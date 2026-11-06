# M0146-0014 — census at fed4f53ed (2026-11-02, run-2 loop #16): exhausted tree=56d7834eecabe2404e0ab8e393a74f7637250eda

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261102g, reason=plan-relevant-change)
— code commit `fed4f53ed` (M0148-0001e slice 1: GROUP BY keys reordered to the input's pathkeys
for the sorted GROUP\_AGG arms; `enable_group_by_reordering` consumed). Its own fire-set gate
measured `fires=none` on all three corpora. `git diff --quiet -- internal cmd scripts bench go.mod
go.sum` held while the capture built its engines, hence `tree=` above. Capture repo head
`d26246bad` (records commit on top of the code commit).

Capture: `BASELINE_REV=91b5b6308 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9q tmp/census-20261102h`
(BASELINE_REV = the commit consumed by routing-20261102g; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9q-candidate`
sha256 `7ebbdb757e611e4464352763512fc1d28c4c20514f002044c7b51c4a0b5d68e7`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261102h/flat` (exit 0):

- `tpch IDENTICAL new=5175c597d5c008062df3cb56dc0f007e5eb92d2bb2019caf9d988cbb384c0f36 base=routing-20261102f/s9o-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 base=routing-20261102f/s9o-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d base=routing-20261102f/s9o-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261102f's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M0148 queue.
