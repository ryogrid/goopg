# M0146-0014 — S9 re-check at 4321d5793 (2026-10-28): exhausted

Trigger: `4321d5793` (M0122-0012 C36, slice 113) changed `internal/executor` and `internal/access/nbtree`: the skip
scan's leaf walk looks ahead within a page (PG 18 `_bt_checkkeys_look_ahead`). Plan-neutral by construction.

| corpus | capture | match | before (b68540921) |
|---|---|---|---|
| TPC-H | `s9ay-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9ay-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9ay-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
