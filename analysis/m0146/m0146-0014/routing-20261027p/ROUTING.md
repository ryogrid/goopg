# M0146-0014 — S9 re-check at b1f699edf (2026-10-27): exhausted

Trigger: `b1f699edf` (M0122-0012 slice 94) changed `internal/optimizer` after the `fb6c415f1` pass: inner-unique
verdicts are memoised for the join search.

| corpus | capture | match | before (fb6c415f1) |
|---|---|---|---|
| TPC-H | `s9ag-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9ag-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9ag-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
