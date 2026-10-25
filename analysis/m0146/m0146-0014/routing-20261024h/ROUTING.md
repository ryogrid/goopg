# M0146-0014 — S9 re-check at 50ecb096b (2026-10-24): exhausted

Trigger: `50ecb096b` changed `internal/executor` after the `b63f03791` pass. That commit is M0147-0012: the serial
bitmap-heap-scan look-ahead window (`GOOPG_HEAP_PREFETCH_DEPTH`) is retired.

| corpus | capture | match | before (b63f03791) |
|---|---|---|---|
| TPC-H | `s9h-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9h-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9h-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
