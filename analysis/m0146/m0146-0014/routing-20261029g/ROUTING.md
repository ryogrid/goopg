# M0146-0014 — census at c49bd9b71 (2026-10-29): exhausted

Trigger: `c49bd9b71` (M0122-0021 residual 4) changed `internal/executor`. Index, index-only and bitmap heap probes
now count their own Cond rejections (`Rows Removed by Filter`). The change is plan-neutral by construction, because it
touches only the instrumentation. The census was captured in the same loop so that the next loop's S9 check can reuse
it.

| corpus | capture | match | before (d38dd96ed, routing-20261029f) |
|---|---|---|---|
| TPC-H | `s9w7-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9w7-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9w7-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
