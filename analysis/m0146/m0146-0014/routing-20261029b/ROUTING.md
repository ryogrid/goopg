# M0146-0014 — census at 28a5ff4c9 (2026-10-29): exhausted

Trigger: `28a5ff4c9` (M0122-0021 slice 2) changed `internal/executor`: the nested-loop index join's Memoize and
probe are instrumented for EXPLAIN ANALYZE. Plan-neutral by construction (reporting only). Captured in the same
loop so the next loop's S9 check can reuse it.

| corpus | capture | match | before (59adcaea7, routing-20261029a) |
|---|---|---|---|
| TPC-H | `s9w2-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9w2-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9w2-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
