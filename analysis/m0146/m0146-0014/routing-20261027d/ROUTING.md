# M0146-0014 — S9 re-check at abd93c55a (2026-10-27): exhausted

Trigger: `abd93c55a` changed `internal/optimizer` after the `77d4c33b6` pass. That commit is M0147-0043: the third
ORDERED arm switch (`GOOPG_INCREMENTAL_SORT`) is retired.

| corpus | capture | match | before (77d4c33b6) |
|---|---|---|---|
| TPC-H | `s9u-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9u-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9u-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
