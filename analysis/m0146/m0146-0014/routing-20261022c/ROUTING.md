# M0146-0014 — S9 re-check at 043bddd4c (2026-10-22): exhausted

Trigger: `043bddd4c` (M0122-0012 slice 38, a CTE scan returns one slot) touched `internal/executor` after the
`b6e96eb84` pass.

| corpus | capture | match | before (b6e96eb84) |
|---|---|---|---|
| TPC-H | `s9c-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9c-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9c-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
