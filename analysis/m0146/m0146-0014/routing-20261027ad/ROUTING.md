# M0146-0014 — S9 re-check at 3d50052e6 (2026-10-27): exhausted

Trigger: `3d50052e6` (M0122-0012 slice 111) changed `internal/executor` after the `b996b60d9` pass: integer
variance and stddev are finalised in fixed width.

| corpus | capture | match | before (b996b60d9) |
|---|---|---|---|
| TPC-H | `s9au-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9au-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9au-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
