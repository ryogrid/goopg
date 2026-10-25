# M0146-0014 — S9 re-check at 44017260b (2026-10-23): exhausted

Trigger: `44017260b` changed `internal/executor` after the `e9ba4cf5f` pass. That commit is M0122-0012 slice 85: a
Gather worker shipping whole rows reads a join's VirtualSlot once.

| corpus | capture | match | before (e9ba4cf5f) |
|---|---|---|---|
| TPC-H | `s9x-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9x-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9x-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
