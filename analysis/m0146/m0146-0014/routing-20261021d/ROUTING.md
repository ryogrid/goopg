# M0146-0014 — S9 re-check at b5832b221 (2026-10-21): exhausted

Trigger: `b5832b221` (M0119 visibility-map WAL slice 2) touched `internal/executor` (VM clear
call sites) after the `943cbd7b1` pass.

| corpus | capture | match | before (943cbd7b1) |
|---|---|---|---|
| TPC-H | `s9d-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9d-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9d-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
