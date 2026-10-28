# M0146-0014 — S9 re-check at 5a89cc5f5 (2026-10-28): exhausted

Trigger: `5a89cc5f5` (M0122-0012 C60, slice 127) changed `internal/executor`: the hashed aggregate's int-lane groups
live in an open-addressing table. Plan-neutral by construction.

| corpus | capture | match | before (b0f3750d3, routing-20261028q) |
|---|---|---|---|
| TPC-H | `s9c60-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9c60-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9c60-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
