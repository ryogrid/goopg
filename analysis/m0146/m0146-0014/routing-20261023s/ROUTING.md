# M0146-0014 — S9 re-check at afdfe9869 (2026-10-23): exhausted

Trigger: `afdfe9869` (M0122-0012 slice 80: an index scan forms its probe key from resolved columns and re-aims one
cursor per rescan) touched `internal/executor` after the `71a749a8e` pass.

| corpus | capture | match | before (71a749a8e) |
|---|---|---|---|
| TPC-H | `s9s-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9s-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9s-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
