# M0146-0014 — S9 re-check at 16101a6a1 (2026-10-27): exhausted

Trigger: `16101a6a1` changed `internal/optimizer` after the `61b3b0dbe` pass. That commit is M0147-0040: the
build-side narrowing switch (`GOOPG_NARROW_BUILD`) is retired.

| corpus | capture | match | before (61b3b0dbe) |
|---|---|---|---|
| TPC-H | `s9r-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9r-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9r-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
