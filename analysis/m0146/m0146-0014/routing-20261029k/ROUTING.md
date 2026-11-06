# M0146-0014 — census at 28c6de6c8 (2026-10-29): exhausted

Trigger: `28c6de6c8` (M0122-0021 residual 14) changed `internal/executor`. A Bitmap Heap Scan now counts its
index-qual recheck rejections and prints them as `Rows Removed by Index Recheck`. The change is plan-neutral by
construction, because it touches only the instrumentation. The census was captured in the same loop so that the next
loop's S9 check can reuse it.

| corpus | capture | match | before (7ad6a9d6b, routing-20261029j) |
|---|---|---|---|
| TPC-H | `s9w11-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9w11-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9w11-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
