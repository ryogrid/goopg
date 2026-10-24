# M0146-0014 — S9 re-check at b63f03791 (2026-10-24): exhausted

Trigger: `b63f03791` changed `internal/executor` after the `1f05de2c5` pass. That commit is M0147-0011: the packed
sort retention (`GOOPG_SORT_PACKED`) is retired.

| corpus | capture | match | before (1f05de2c5) |
|---|---|---|---|
| TPC-H | `s9g-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9g-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9g-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
