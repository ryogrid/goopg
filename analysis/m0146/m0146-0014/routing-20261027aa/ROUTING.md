# M0146-0014 — S9 re-check at 83bfa2945 (2026-10-27): exhausted

Trigger: `83bfa2945` (M0122-0012 slice 107) changed `internal/executor` after the `8d794539a` pass (`acd4ff2d7` is
ralph/docs only): a grouping-sets aggregate evaluates each argument once per row.

| corpus | capture | match | before (8d794539a) |
|---|---|---|---|
| TPC-H | `s9ar-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9ar-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9ar-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
