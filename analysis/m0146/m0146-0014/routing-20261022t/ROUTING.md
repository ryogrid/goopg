# M0146-0014 — S9 re-check at 0f365344a (2026-10-22): exhausted

Trigger: `0f365344a` (M0122-0012 slice 58, the serial leader path of a sorted Group stops copying and allocating
per row) touched `internal/executor` after the `49da13013` pass.

| corpus | capture | match | before (49da13013) |
|---|---|---|---|
| TPC-H | `s9t-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9t-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9t-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
