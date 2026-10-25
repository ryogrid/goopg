# M0146-0014 — S9 re-check at a05a55628 (2026-10-23): exhausted

Trigger: `a05a55628` (M0122-0012 slice 83: a sort read only by a Gather Merge stores compact rows) touched
`internal/executor` after the `233658626` pass.

| corpus | capture | match | before (233658626) |
|---|---|---|---|
| TPC-H | `s9v-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9v-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9v-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
