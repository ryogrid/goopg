# M0146-0014 — S9 re-check at 169a92964 (2026-10-21): exhausted

Trigger: `169a92964` (M0122-0012 slice 27, in-place same-kind comparison) touched `internal/executor` after the
`7909e3796` pass (mmgr-only, census reused).

| corpus | capture | match | before (493de93b5) |
|---|---|---|---|
| TPC-H | `s9m-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9m-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9m-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
