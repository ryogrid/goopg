# M0146-0014 — S9 re-check at f8a8392fd (2026-10-26): exhausted

Trigger: `f8a8392fd` changed `internal/executor` after the `eff6e2294` pass. That commit is M0147-0037: the hashed
IN-probe switch (`GOOPG_HASHED_SUBPLAN`) is retired, with a test-only force-linear seam kept.

| corpus | capture | match | before (eff6e2294) |
|---|---|---|---|
| TPC-H | `s9o-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9o-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9o-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
