# M0146-0014 — S9 re-check at a4c64c462 (2026-10-26): exhausted

Trigger: `a4c64c462` changed `internal/optimizer` after the `9c3e12d47` pass. That commit is M0147-0020: the
C-19e partial-sort tournament (`GOOPG_PARTIAL_SORT_PATHS`) is promoted.

| corpus | capture | match | before (9c3e12d47) |
|---|---|---|---|
| TPC-H | `s9f-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9f-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9f-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
