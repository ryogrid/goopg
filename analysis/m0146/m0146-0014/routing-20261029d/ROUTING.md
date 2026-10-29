# M0146-0014 — census at b93ad5337 (2026-10-29): exhausted

Trigger: `b93ad5337` (M0122-0021 slice 4) changed `internal/executor`: the Gather's shared hash prebuild and worker
lazily built subtrees are instrumented for EXPLAIN ANALYZE. Plan-neutral by construction (reporting only).
Captured in the same loop so the next loop's S9 check can reuse it.

| corpus | capture | match | before (132bf92d2, routing-20261029c) |
|---|---|---|---|
| TPC-H | `s9w4-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9w4-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9w4-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
