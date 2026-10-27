# M0146-0014 — S9 re-check at b7d73466c (2026-10-27): exhausted

Trigger: `54223610d` (M0147-0053) changed `internal/optimizer` and `internal/executor` (tests) after the `5a9b2fb84`
pass: an outer join's preserved-side-only ON conjunct is now held at the join instead of declining the search.
`b7d73466c` above it touched only fix_plan.

| corpus | capture | match | before (5a9b2fb84) |
|---|---|---|---|
| TPC-H | `s9aa-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9aa-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9aa-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
