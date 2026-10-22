# M0146-0014 — S9 re-check at 19610b0a5 (2026-10-21): exhausted

Trigger: `19610b0a5` (M0122-0012 slice 24, int64 group lane) touched `internal/executor` after the
`4dba3357b` pass.

| corpus | capture | match | before (4dba3357b) |
|---|---|---|---|
| TPC-H | `s9k-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9k-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9k-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
