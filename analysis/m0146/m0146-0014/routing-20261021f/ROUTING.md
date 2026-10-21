# M0146-0014 — S9 re-check at 2181e6060 (2026-10-21): exhausted

Trigger: `2181e6060` (M0122-0012 slice 19, scan column skip) touched `internal/executor` after the
`a9a9b1f55` pass.

| corpus | capture | match | before (a9a9b1f55) |
|---|---|---|---|
| TPC-H | `s9f-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9f-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9f-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
