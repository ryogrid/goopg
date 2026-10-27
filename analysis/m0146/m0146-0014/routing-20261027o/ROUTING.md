# M0146-0014 — S9 re-check at fb6c415f1 (2026-10-27): exhausted

Trigger: `fb6c415f1` (M0122-0012 slice 93) changed `internal/optimizer` after the `102bc10a4` pass: the join search
memoises its narrowed-width inputs.

| corpus | capture | match | before (102bc10a4) |
|---|---|---|---|
| TPC-H | `s9af-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9af-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9af-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
