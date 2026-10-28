# M0146-0014 — S9 re-check at c9acfdee9 (2026-10-28): exhausted

Trigger: `c9acfdee9` (M0122-0012 C57, slice 124) changed `internal/executor`: a seq scan read only by an aggregate
chain decodes only the aggregate's columns. Plan-neutral by construction (executor read sets only).

| corpus | capture | match | before (d0f1515c0, routing-20261028n) |
|---|---|---|---|
| TPC-H | `s9c57-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9c57-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9c57-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
