# M0146-0014 — S9 re-check at b996b60d9 (2026-10-27): exhausted

Trigger: `b996b60d9` (M0122-0012 slice 110) changed `internal/executor` after the `a8367e529` pass (`2910f8451` is
analysis-only): the ordered-merge comparator reads key Datums in place.

| corpus | capture | match | before (a8367e529) |
|---|---|---|---|
| TPC-H | `s9at-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9at-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9at-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
