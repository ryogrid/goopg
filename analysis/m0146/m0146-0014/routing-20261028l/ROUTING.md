# M0146-0014 — S9 re-check at d6476dbc4 (2026-10-28): exhausted

Trigger: `d6476dbc4` (M0122-0012 C50, slice 121) changed `internal/executor`: a searched CASE consumes its WHEN
comparison as a bool. Plan-neutral by construction.

| corpus | capture | match | before (49db3ffa9) |
|---|---|---|---|
| TPC-H | `s9bg-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9bg-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9bg-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
