# M0146-0014 — S9 re-check at f39f376e9 (2026-10-22): exhausted

Trigger: `f39f376e9` (M0122-0012 slice 36, re-opened index probes keep their decode buffer) touched
`internal/executor` after the `7f5ce4a07` pass.

| corpus | capture | match | before (7f5ce4a07) |
|---|---|---|---|
| TPC-H | `s9a-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9a-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9a-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
