# M0146-0014 — S9 re-check at 0928e85b3 (2026-10-22): exhausted

Trigger: `0928e85b3` (M0122-0012 slice 50, hashed literal IN lists) touched
`internal/executor` after the `5bb2a9977` pass.

| corpus | capture | match | before (5bb2a9977) |
|---|---|---|---|
| TPC-H | `s9n-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9n-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9n-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
