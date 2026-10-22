# M0146-0014 — S9 re-check at b6e96eb84 (2026-10-22): exhausted

Trigger: `b6e96eb84` (M0122-0012 slice 37, borrowed nested loops emit their pair buffer) touched
`internal/executor` after the `f39f376e9` pass. The captures were built from the working tree, which also held
the uncommitted slice 38 change (`cteScanOp` / `materializedCTEScanOp` reuse one slot). That change is
EXPLAIN-neutral: it touches no planner code and no plan-visible operator state.

| corpus | capture | match | before (f39f376e9) |
|---|---|---|---|
| TPC-H | `s9b-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9b-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9b-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
