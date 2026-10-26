# M0146-0014 — S9 re-check at 6c45b63bd (2026-10-27): exhausted

Trigger: `6c45b63bd` changed `internal/optimizer` after the `16101a6a1` pass. That commit is M0147-0041: the
cost-input narrowing switch (`GOOPG_NARROW_COST_INPUTS`) is retired.

| corpus | capture | match | before (16101a6a1) |
|---|---|---|---|
| TPC-H | `s9s-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9s-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9s-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
