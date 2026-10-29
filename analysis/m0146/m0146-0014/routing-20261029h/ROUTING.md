# M0146-0014 — census at 910e30183 (2026-10-29): exhausted

Trigger: `910e30183` (M0122-0021 residual 3) changed `internal/executor`. Below a Gather, each worker's Memoize
counters now print as a `Worker N:  Hits:` line. The change is plan-neutral by construction, because it touches only
the instrumentation. The census was captured in the same loop so that the next loop's S9 check can reuse it.

| corpus | capture | match | before (c49bd9b71, routing-20261029g) |
|---|---|---|---|
| TPC-H | `s9w8-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9w8-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9w8-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
