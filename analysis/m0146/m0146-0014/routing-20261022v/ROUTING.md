# M0146-0014 — S9 re-check at fdb607c10 (2026-10-22): exhausted

Trigger: `fdb607c10` (M0122-0012 slice 60, string sort keys compare plainly and DISTINCT ON keys stop allocating
per row) touched `internal/executor` after the `5d6e91613` pass.

| corpus | capture | match | before (5d6e91613) |
|---|---|---|---|
| TPC-H | `s9v-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9v-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9v-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
