# M0146-0014 — S9 re-check at 5bb2a9977 (2026-10-22): exhausted

Trigger: `5bb2a9977` (M0122-0012 slice 49, skip-scan group keys without index re-resolution) touched
`internal/executor` after the `18b8ea331` pass.

| corpus | capture | match | before (18b8ea331) |
|---|---|---|---|
| TPC-H | `s9m-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9m-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9m-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
