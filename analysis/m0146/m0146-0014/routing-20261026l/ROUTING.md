# M0146-0014 — S9 re-check at eff6e2294 (2026-10-26): exhausted

Trigger: `eff6e2294` changed `internal/executor` after the `7608c9ff0` pass. That commit is M0147-0036: the legacy
per-call SubPlan lifecycle switch (`GOOPG_SUBPLAN_RESCAN`) is retired.

| corpus | capture | match | before (7608c9ff0) |
|---|---|---|---|
| TPC-H | `s9n-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9n-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9n-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
