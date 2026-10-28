# M0146-0014 — S9 re-check at 102c74a5e (2026-10-28): exhausted

Trigger: `102c74a5e` (M0122-0012 C46, slice 118) changed `internal/executor`: the aggregate finalize path reads the
group state and the call through pointers. Plan-neutral by construction.

| corpus | capture | match | before (923750127) |
|---|---|---|---|
| TPC-H | `s9bd-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9bd-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9bd-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
