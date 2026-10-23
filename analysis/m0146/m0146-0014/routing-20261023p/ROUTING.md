# M0146-0014 — S9 re-check at a5693f85c (2026-10-23): exhausted

Trigger: `a5693f85c` (M0122-0012 slice 77: one cursor per skip scan, re-aimed per descent) touched
`internal/executor` (and `internal/access/nbtree`) after the `88998a861` pass.

| corpus | capture | match | before (88998a861) |
|---|---|---|---|
| TPC-H | `s9p-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9p-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9p-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
