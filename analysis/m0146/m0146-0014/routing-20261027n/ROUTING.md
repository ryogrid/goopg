# M0146-0014 — S9 re-check at 102bc10a4 (2026-10-27): exhausted

Trigger: `102bc10a4` (M0122-0019) changed `internal/optimizer` and `internal/executor` after the `da7f0c117` pass:
the planner stamps its duration on the Explain node for EXPLAIN's Planning Time.

| corpus | capture | match | before (da7f0c117) |
|---|---|---|---|
| TPC-H | `s9ae-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9ae-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9ae-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
