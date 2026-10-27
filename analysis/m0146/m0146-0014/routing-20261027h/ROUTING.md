# M0146-0014 — S9 re-check at b2665b45c (2026-10-27): exhausted

Trigger: `b2665b45c` changed `internal/optimizer` and `internal/executor` (tests) after the `3a8bfaa96` pass. That
commit is M0147-0047: the size-rule split verdict (`GOOPG_PARTIAL_AGG_PATHS`) is retired.

| corpus | capture | match | before (3a8bfaa96) |
|---|---|---|---|
| TPC-H | `s9y-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9y-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9y-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
