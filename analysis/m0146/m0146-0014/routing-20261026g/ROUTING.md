# M0146-0014 — S9 re-check at 63d2ab399 (2026-10-26): exhausted

Trigger: `63d2ab399` changed `internal/optimizer` after the `4f7b216cd` pass (`e5f10324e` in between was
analysis-only). That commit is M0147-0031: the join-leg narrowing switch (`GOOPG_NARROW_LEG_HOOK`) is retired.

| corpus | capture | match | before (4f7b216cd) |
|---|---|---|---|
| TPC-H | `s9i-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9i-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9i-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
