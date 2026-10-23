# M0146-0014 — S9 re-check at cb3e7e6b6 (2026-10-23): exhausted

Trigger: `cb3e7e6b6` (M0122-0012 slice 73, the Parallel Hash build-side claim walk passes a partial Unique) touched
`internal/executor` after the `d92a36e6f` pass.

| corpus | capture | match | before (d92a36e6f) |
|---|---|---|---|
| TPC-H | `s9m-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9m-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9m-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
