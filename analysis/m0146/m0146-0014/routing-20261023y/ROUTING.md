# M0146-0014 — S9 re-check at cd37060e5 (2026-10-23): exhausted

Trigger: `cd37060e5` changed `internal/executor` after the `44017260b` pass. That commit is M0122-0012 slice 86: a
seq scan judges the qual's leading conjunct on its own columns.

| corpus | capture | match | before (44017260b) |
|---|---|---|---|
| TPC-H | `s9y-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9y-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9y-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
