# M0146-0014 — S9 re-check at 529af8c85 (2026-10-26): exhausted

Trigger: `529af8c85` changed `internal/optimizer` after the `f8a8392fd` pass. That commit is M0147-0038: the upper-Sort
narrowing site switch (`GOOPG_NARROW_UPPER_SORT`) is retired.

| corpus | capture | match | before (f8a8392fd) |
|---|---|---|---|
| TPC-H | `s9p-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9p-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9p-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
