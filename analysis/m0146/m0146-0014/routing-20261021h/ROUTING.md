# M0146-0014 — S9 re-check at 022774301 (2026-10-21): exhausted

Trigger: `022774301` (M0122-0012 slice 21, early value-list IN + literal-list cache) touched `internal/optimizer` and `internal/executor` after the
`54a37edcd` pass.

| corpus | capture | match | before (54a37edcd) |
|---|---|---|---|
| TPC-H | `s9h-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9h-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9h-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
