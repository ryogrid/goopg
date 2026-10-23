# M0146-0014 — S9 re-check at 3a50b12ed (2026-10-23): exhausted

Trigger: `3a50b12ed` (M0122-0012 slice 66, liveness crosses a Project and a join's probe side, and a Gather worker
carves its shipped rows from chunks) touched `internal/executor` and `internal/optimizer` after the `d4a855738` pass.

| corpus | capture | match | before (d4a855738) |
|---|---|---|---|
| TPC-H | `s9e-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9e-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9e-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
