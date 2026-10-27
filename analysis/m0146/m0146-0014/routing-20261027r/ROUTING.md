# M0146-0014 — S9 re-check at 660e20d86 (2026-10-27): exhausted

Trigger: `660e20d86` (M0122-0012 slice 96) changed `internal/optimizer` after the `c0d1f97ed` pass: a join rel's
column-width map is formed on first read.

| corpus | capture | match | before (c0d1f97ed) |
|---|---|---|---|
| TPC-H | `s9ai-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9ai-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9ai-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
