# M0146-0014 — S9 re-check at dd246d001 (2026-10-27): exhausted

Trigger: `dd246d001` changed `internal/optimizer` and `internal/executor` (tests) after the `abd93c55a` pass. That
commit is M0147-0044: the process-wide Memoize switch (`GOOPG_MEMOIZE`) is retired.

| corpus | capture | match | before (abd93c55a) |
|---|---|---|---|
| TPC-H | `s9v-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9v-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9v-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
