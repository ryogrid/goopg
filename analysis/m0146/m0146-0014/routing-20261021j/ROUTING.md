# M0146-0014 — S9 re-check at 4dba3357b (2026-10-21): exhausted

Trigger: `4dba3357b` (M0122-0012 slice 23, build-side and Gather-worker borrow roots) touched `internal/executor` after the
`f24bff055` pass.

| corpus | capture | match | before (f24bff055) |
|---|---|---|---|
| TPC-H | `s9j-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9j-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9j-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
