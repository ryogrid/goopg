# M0146-0014 — S9 re-check at 8d794539a (2026-10-27): exhausted

Trigger: `8d794539a` (M0122-0012 slice 106) changed `internal/executor` after the `08a69974e` pass: the sort
comparator reads key Datums in place.

| corpus | capture | match | before (08a69974e) |
|---|---|---|---|
| TPC-H | `s9aq-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9aq-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9aq-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
