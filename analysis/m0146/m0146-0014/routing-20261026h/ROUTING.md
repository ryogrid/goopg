# M0146-0014 — S9 re-check at 8ab965464 (2026-10-26): exhausted

Trigger: `8ab965464` changed `internal/executor` after the `63d2ab399` pass. That commit is M0147-0032: the
cooperative join-tree build switch (`GOOPG_COOP_JOIN_BUILD`) is retired.

| corpus | capture | match | before (63d2ab399) |
|---|---|---|---|
| TPC-H | `s9j-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9j-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9j-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
