# M0146-0014 — S9 re-check at 4cf358ae6 (2026-10-24): exhausted

Trigger: `4cf358ae6` changed `internal/executor` after the `e8bf90456` pass. That commit is M0122-0012 slice 90: the
plain-string test and the string read inline at the comparison.

| corpus | capture | match | before (e8bf90456) |
|---|---|---|---|
| TPC-H | `s9c-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9c-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9c-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
