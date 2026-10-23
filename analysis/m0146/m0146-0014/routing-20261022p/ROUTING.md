# M0146-0014 — S9 re-check at 48af8abcc (2026-10-22): exhausted

Trigger: `48af8abcc` (M0122-0012 slice 52, skip-scan group keys from entry images) touched
`internal/executor` and `internal/access/nbtree` after the `b535f1ec5` pass.

| corpus | capture | match | before (b535f1ec5) |
|---|---|---|---|
| TPC-H | `s9p-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9p-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9p-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
