# M0146-0014 — S9 re-check at c53226b14 (2026-10-21): exhausted

Trigger: `c53226b14` (M0122-0012 slice 29, filtered scans walk past unread columns) touched `internal/executor` after the
`d857ea0b9` pass.

| corpus | capture | match | before (d857ea0b9) |
|---|---|---|---|
| TPC-H | `s9o-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9o-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9o-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
