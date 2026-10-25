# M0146-0014 — S9 re-check at 8c2d59d66 (2026-10-24): exhausted

Trigger: `8c2d59d66` changed `internal/executor` after the `127b21ea8` pass. That commit is M0122-0012 slice 88: a
Gather Merge read by a borrowing aggregate ships compact rows.

| corpus | capture | match | before (127b21ea8) |
|---|---|---|---|
| TPC-H | `s9a-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9a-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9a-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
