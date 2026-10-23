# M0146-0014 — S9 re-check at c6a780f7e (2026-10-23): exhausted

Trigger: `c6a780f7e` (M0122-0012 slice 62, an explicit-frame window aggregate keeps its running state) touched
`internal/executor` after the `fdb607c10` pass (`edc87a31a` in between touched only `internal/access/nbtree`).

| corpus | capture | match | before (fdb607c10) |
|---|---|---|---|
| TPC-H | `s9a-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9a-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9a-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
