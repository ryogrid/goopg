# M0146-0014 — S9 re-check at d857ea0b9 (2026-10-21): exhausted

Trigger: `d857ea0b9` (M0122-0012 slice 28, borrow through nested loops) touched `internal/executor` after the
`169a92964` pass.

| corpus | capture | match | before (169a92964) |
|---|---|---|---|
| TPC-H | `s9n-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9n-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9n-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
