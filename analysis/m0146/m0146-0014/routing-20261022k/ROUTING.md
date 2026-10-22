# M0146-0014 — S9 re-check at b57f3f698 (2026-10-22): exhausted

Trigger: `b57f3f698` (M0122-0012 slice 47, CASE compiles) touched
`internal/executor` after the `f78898a0d` pass.

| corpus | capture | match | before (f78898a0d) |
|---|---|---|---|
| TPC-H | `s9k-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9k-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9k-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
