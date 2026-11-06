# M0146-0014 — census at 132bf92d2 (2026-10-29): exhausted

Trigger: `132bf92d2` (M0122-0021 slice 3) changed `internal/executor`: CTE bodies, SubPlans/InitPlans and
cooperative hash-build inputs are instrumented for EXPLAIN ANALYZE. Plan-neutral by construction (reporting only).
Captured in the same loop so the next loop's S9 check can reuse it.

| corpus | capture | match | before (28a5ff4c9, routing-20261029b) |
|---|---|---|---|
| TPC-H | `s9w3-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9w3-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9w3-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
