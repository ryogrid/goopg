# M0146-0014 — S9 re-check at 1f05de2c5 (2026-10-24): exhausted

Trigger: `1f05de2c5` changed `internal/executor` and `internal/storage` after the `2e8519eff` pass. That commit is
M0147-0010: the S30.3 / S32.x diagnostic probes are retired.

| corpus | capture | match | before (2e8519eff) |
|---|---|---|---|
| TPC-H | `s9f-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9f-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9f-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
