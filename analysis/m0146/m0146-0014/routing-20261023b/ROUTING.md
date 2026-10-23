# M0146-0014 — S9 re-check at ecbfe6696 (2026-10-23): exhausted

Trigger: `ecbfe6696` (M0122-0012 slice 63, a Gather Merge under an aggregate ships only live columns) touched
`internal/executor` after the `c6a780f7e` pass.

| corpus | capture | match | before (c6a780f7e) |
|---|---|---|---|
| TPC-H | `s9b-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9b-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9b-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
