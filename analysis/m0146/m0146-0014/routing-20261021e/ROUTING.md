# M0146-0014 — S9 re-check at a9a9b1f55 (2026-10-21): exhausted

Trigger: `a9a9b1f55` (M0122-0012 slice 18, scan row borrow) touched `internal/executor` after the
`b5832b221` pass.

| corpus | capture | match | before (b5832b221) |
|---|---|---|---|
| TPC-H | `s9e-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9e-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9e-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
