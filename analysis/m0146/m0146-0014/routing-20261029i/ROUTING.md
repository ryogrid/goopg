# M0146-0014 — census at 431cec8d7 (2026-10-29): exhausted

Trigger: `431cec8d7` (M0122-0021 residual 10) changed `internal/executor`. A Bitmap Heap Scan now prints PG's
`Heap Blocks:` line for the leader and for each worker. The change is plan-neutral by construction, because it touches
only the instrumentation. The census was captured in the same loop so that the next loop's S9 check can reuse it.

| corpus | capture | match | before (910e30183, routing-20261029h) |
|---|---|---|---|
| TPC-H | `s9w9-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9w9-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9w9-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
