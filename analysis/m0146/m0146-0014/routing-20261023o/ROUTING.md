# M0146-0014 — S9 re-check at 88998a861 (2026-10-23): exhausted

Trigger: `88998a861` (M0122-0012 slice 75: correlated references compile, and a comparison reads its operands in
place) touched `internal/executor` after the `f7dc0cc55` pass.

| corpus | capture | match | before (f7dc0cc55) |
|---|---|---|---|
| TPC-H | `s9o-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9o-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9o-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
