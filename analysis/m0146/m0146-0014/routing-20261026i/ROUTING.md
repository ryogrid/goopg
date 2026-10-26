# M0146-0014 — S9 re-check at 07d3ad3e9 (2026-10-26): exhausted

Trigger: `07d3ad3e9` changed `internal/executor` after the `8ab965464` pass. That commit is M0147-0033: the unbounded
nested-loop inner cache switch (`GOOPG_NL_MATERIALIZE_WORK_MEM`) is retired.

| corpus | capture | match | before (8ab965464) |
|---|---|---|---|
| TPC-H | `s9k-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9k-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9k-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
