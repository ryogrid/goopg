# M0146-0014 — S9 re-check at 5d6e91613 (2026-10-22): exhausted

Trigger: `5d6e91613` (M0122-0012 slice 59, IS NULL compiles and the sort / merge comparators take the same-kind
fast path) touched `internal/executor` after the `0f365344a` pass.

| corpus | capture | match | before (0f365344a) |
|---|---|---|---|
| TPC-H | `s9u-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9u-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9u-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
