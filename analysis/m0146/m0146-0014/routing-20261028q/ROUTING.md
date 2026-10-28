# M0146-0014 — S9 re-check at b0f3750d3 (2026-10-28): exhausted

Trigger: `b0f3750d3` (M0122-0012 C59, slice 126) changed `internal/executor`: the hash join's int-lane probe reads a
flat open-addressing index. Plan-neutral by construction.

| corpus | capture | match | before (5463cb473, routing-20261028p) |
|---|---|---|---|
| TPC-H | `s9c59-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9c59-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9c59-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
