# M0146-0014 — S9 re-check at 59a326fc7 (2026-10-21): exhausted

Trigger: `59a326fc7` (M0122-0012 slice 32, keyed identity compares in pathkey usefulness) touched
`internal/optimizer` after the `5fcdbb6e0` pass.

| corpus | capture | match | before (5fcdbb6e0) |
|---|---|---|---|
| TPC-H | `s9q-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9q-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9q-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
