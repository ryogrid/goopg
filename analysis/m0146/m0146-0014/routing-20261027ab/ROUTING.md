# M0146-0014 — S9 re-check at a8367e529 (2026-10-27): exhausted

Trigger: `a8367e529` (M0122-0012 slice 109) changed `internal/executor` after the `83bfa2945` pass (`e35fff402`
touched nbtree only): a hash aggregate emits its founded groups directly.

| corpus | capture | match | before (83bfa2945) |
|---|---|---|---|
| TPC-H | `s9as-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9as-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9as-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
