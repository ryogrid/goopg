# M0146-0014 — S9 re-check at 59adcaea7 (2026-10-29): exhausted

Trigger: `881b3e314` (M0122-0021 slice 1) changed `internal/executor`: EXPLAIN ANALYZE folds per-site worker
instrumentation onto the node line. Plan-neutral by construction (reporting only).

| corpus | capture | match | before (5a89cc5f5, routing-20261028r) |
|---|---|---|---|
| TPC-H | `s9w1-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9w1-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9w1-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
