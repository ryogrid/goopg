# M0146-0014 — S9 re-check at 32a6ac752 (2026-10-23): exhausted

Trigger: `32a6ac752` (M0122-0016, a big-mantissa numeric owns its payload instead of writing it to the never-reset
permanent arena) touched `internal/executor` after the `97086429c` pass.

| corpus | capture | match | before (97086429c) |
|---|---|---|---|
| TPC-H | `s9h-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9h-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9h-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
