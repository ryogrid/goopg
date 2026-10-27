# M0146-0014 — S9 re-check at 61b3b0dbe (2026-10-26): exhausted

Trigger: `61b3b0dbe` changed `internal/optimizer` after the `529af8c85` pass. That commit is M0147-0039: the
upper-narrowing pass switch (`GOOPG_NARROW_UPPER`) is retired.

| corpus | capture | match | before (529af8c85) |
|---|---|---|---|
| TPC-H | `s9q-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9q-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9q-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
