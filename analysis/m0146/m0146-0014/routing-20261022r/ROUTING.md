# M0146-0014 — S9 re-check at de41f8c20 (2026-10-22): exhausted

Trigger: `de41f8c20` (M0122-0012 slice 56, a fixed-width skip group's keys are copied into templates) touched
`internal/executor` after the `7e6b592d1` pass. Slices 54 and 55 in between touched only `internal/access/nbtree`.

| corpus | capture | match | before (7e6b592d1) |
|---|---|---|---|
| TPC-H | `s9r-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9r-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9r-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
