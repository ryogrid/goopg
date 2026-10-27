# M0146-0014 — S9 re-check at 5a9b2fb84 (2026-10-27): exhausted

Trigger: `5a9b2fb84` changed `internal/optimizer` and `internal/executor` (tests) after the `b2665b45c` pass. That
commit is M0147-0049: the unnest index-key harvest switch (`GOOPG_INDEXKEY_HARVEST`) is retired.

| corpus | capture | match | before (b2665b45c) |
|---|---|---|---|
| TPC-H | `s9z-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9z-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9z-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
