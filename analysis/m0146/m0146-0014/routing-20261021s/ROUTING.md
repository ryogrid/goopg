# M0146-0014 — S9 re-check at 434b23349 (2026-10-21): exhausted

Trigger: `434b23349` (M0122-0012 slice 34, a plain hash aggregate does not sort its output) touched
`internal/executor` after the `b24046088` pass.

| corpus | capture | match | before (b24046088) |
|---|---|---|---|
| TPC-H | `s9s-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9s-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9s-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
