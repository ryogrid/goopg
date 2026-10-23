# M0146-0014 — S9 re-check at 127b21ea8 (2026-10-24): exhausted

Trigger: `127b21ea8` changed `internal/executor` after the `cd37060e5` pass. That commit is M0122-0012 slice 87: a
sorted aggregate carries the next group in the finalized group's runtime.

| corpus | capture | match | before (cd37060e5) |
|---|---|---|---|
| TPC-H | `s9z-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9z-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9z-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
