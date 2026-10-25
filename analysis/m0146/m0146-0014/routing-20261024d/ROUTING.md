# M0146-0014 — S9 re-check at 8532a71e3 (2026-10-24): exhausted

Trigger: `8532a71e3` changed `internal/optimizer` after the `4cf358ae6` pass. That commit is M0122-0012 slice 91: a
correlated sublink over a CTE scan is lowered.

| corpus | capture | match | before (4cf358ae6) |
|---|---|---|---|
| TPC-H | `s9d-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9d-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9d-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
