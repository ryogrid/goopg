# M0146-0014 — census at 04cc96f3b (2026-10-29): exhausted

Trigger: `04cc96f3b` (M0122-0021 residual 1) changed `internal/executor`: EXPLAIN ANALYZE prints the bare
`SubPlan N` / `InitPlan N` label. Plan-neutral by construction (EXPLAIN ANALYZE text only). Captured in the same
loop so the next loop's S9 check can reuse it.

| corpus | capture | match | before (b93ad5337, routing-20261029d) |
|---|---|---|---|
| TPC-H | `s9w5-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9w5-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9w5-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
