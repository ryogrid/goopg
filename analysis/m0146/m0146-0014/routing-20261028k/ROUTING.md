# M0146-0014 — S9 re-check at 49db3ffa9 (2026-10-28): exhausted

Trigger: `49db3ffa9` (M0122-0012 C49, slice 120) changed `internal/executor` and `internal/access/transam`: the
seq scan's visibility check takes the tuple header and the snapshot by pointer. Plan-neutral by construction.

| corpus | capture | match | before (12f21f440) |
|---|---|---|---|
| TPC-H | `s9bf-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9bf-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9bf-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
