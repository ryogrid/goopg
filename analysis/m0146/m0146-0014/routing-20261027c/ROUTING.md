# M0146-0014 — S9 re-check at 77d4c33b6 (2026-10-27): exhausted

Trigger: `77d4c33b6` changed `internal/optimizer` after the `6c45b63bd` pass. That commit is M0147-0042: the Sort
byte-currency switch (`GOOPG_PG_SORT_RELATION_BYTES_COST`) is retired.

| corpus | capture | match | before (6c45b63bd) |
|---|---|---|---|
| TPC-H | `s9t-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9t-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9t-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
