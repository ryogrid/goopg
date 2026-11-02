# M0146-0014 — census at 5d9870a21 (2026-11-02, run-2 loop #26): exhausted tree=499b5db46e79c660c336d23a78d2dbd3e62cd2b2

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261102p, reason=plan-relevant-change)
— code commit `5d9870a21` (M0148-0001f slice 4: generate\_series FROM items carry their RTE's RTID
so EXPLAIN names their columns). No corpus query scans generate\_series, so its own fire-set gate
measured `fires=none` on all three corpora. `git diff --quiet -- internal cmd scripts bench go.mod
go.sum` held while the capture built its engines, hence `tree=` above. Capture repo head `202aa455e`
(records commit on top of the code commit).

Capture: `BASELINE_REV=82c45ed82 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9z tmp/census-20261102q`
(BASELINE_REV = the head consumed by routing-20261102p; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9z-candidate`
sha256 `65a17dd62184c11eb7ef78a23b35be3ba41a6f69ac0a0e8e7b5cca37fb29856f`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261102q/flat` (exit 0):

- `tpch IDENTICAL new=5175c597d5c008062df3cb56dc0f007e5eb92d2bb2019caf9d988cbb384c0f36 base=routing-20261102f/s9o-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 base=routing-20261102f/s9o-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d base=routing-20261102f/s9o-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261102f's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M0148 queue.
