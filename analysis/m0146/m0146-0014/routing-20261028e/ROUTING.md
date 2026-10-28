# M0146-0014 — S9 re-check at f645129a8 (2026-10-28): exhausted

Trigger: `f645129a8` (M0122-0012 C37, slice 114) changed `internal/access/nbtree` and `internal/executor`: signed-integer
btree key attributes are compared inline. Plan-neutral by construction.

| corpus | capture | match | before (4321d5793) |
|---|---|---|---|
| TPC-H | `s9az-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9az-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9az-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
