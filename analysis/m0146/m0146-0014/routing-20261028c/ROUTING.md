# M0146-0014 — S9 re-check at b68540921 (2026-10-28): exhausted

Trigger: `b68540921` (M0122-0012 C35, slice 112) changed `internal/executor`: `decodeRowRangeInfo` walks a run of
consecutive skipped columns in one loop. Plan-neutral by construction.

| corpus | capture | match | before (39041efc5) |
|---|---|---|---|
| TPC-H | `s9ax-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9ax-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9ax-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
