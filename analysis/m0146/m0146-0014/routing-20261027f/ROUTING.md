# M0146-0014 — S9 re-check at 637799d73 (2026-10-27): exhausted

Trigger: `637799d73` changed `internal/optimizer` and `internal/executor` (tests) after the `dd246d001` pass. That
commit is M0147-0045: the process-wide parallel switch (`GOOPG_PARALLEL`) is retired.

| corpus | capture | match | before (dd246d001) |
|---|---|---|---|
| TPC-H | `s9w-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9w-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9w-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
