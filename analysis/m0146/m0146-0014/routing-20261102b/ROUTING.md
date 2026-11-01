# M0146-0014 — census at 359e35c97 (2026-11-02, run-2 loop #10): exhausted tree=a69a3846615c1e37502d575a86e882ab80db37ac

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261102a, reason=plan-relevant-change)
— code commit `359e35c97`: the pg_statistic_ext.stxkind catalog-stub fix (psql `\d` 42809,
AI-20261102-000225-003) together with M0148-0001c slice (d) (equality-pinned min/max InitPlan
`Limit → Seq Scan`, `minmaxArgPinnedByEquality`). Neither corpus queries pg_statistic_ext nor
runs a min/max over an equality-pinned unindexed column, so `fires=none` is expected and was
measured. `git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture
built its engines (only bookkeeping was dirty), hence `tree=` above. Capture repo head
`fd5c26098` (the slice's records commit on top of the code commit).

Capture: `BASELINE_REV=68997af77 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9k tmp/census-20261102b`
(BASELINE_REV = the commit consumed by routing-20261102a; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9k-candidate`
sha256 `b4f181c4a1411564c8425a67ae98180caf34c5714a89dc0a5aeef3be79b4b73b`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261102b/flat` (exit 0):

- `tpch IDENTICAL new=ce932e73bc646988fbc215145f90119edbb233bdaf1884af4402442a6ba28421 base=routing-20261030c/s9w17-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 base=routing-20261030c/s9w17-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d base=routing-20261030c/s9w17-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261102a's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M0148 queue (0001c2, 0001d — patch ready).
