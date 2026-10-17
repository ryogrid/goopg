# M0146-0014 — S9 pass at 4334a3714 (2026-10-16): files M0146-0217

Trigger: every item-3 task is gated. M0146-0214 is blocked-on M0146-0216, M0146-0215 waits on the owner's stats
import, and M0146-0216 on the owner's index refresh. Executor and planner code changed after the last census
(f92c69825, 93cf7589b, 4334a3714), so the census was captured fresh.

| corpus | capture | match |
|---|---|---|
| TPC-H | `s9y-tpch*` | 22 / 22 |
| TPC-DS SF0.25 | `s9y-tpcds-sf025*` | 86 / 99 |
| TPC-DS SF1 | `s9y-tpcds-sf1*` | 74 / 99 |

The match counts are unchanged from routing-20261016x. Since the owner's 2026-10-16 directive un-waived the
residual classes, every non-MATCH record is actionable unless a live task owns it: Q64 (M0146-0214), Q76
(M0146-0215) and Q52 (M0146-0216).

## Ranking (first-divergence category, both TPC-DS corpora)

| category | actionable records |
|---|---|
| sort-strategy | 8 (SF0.25 Q4, Q38, Q72, Q87; SF1 Q18, Q38, Q56, Q72) |
| join-order | 8 (SF0.25 Q99; SF1 Q4, Q11, Q31, Q62, Q77, Q78, Q99) |
| aggregation-strategy | 7 |
| parallelism | 5 |
| qual-placement, scan-type | 2 each |
| join-method | 1 |

The tie goes to sort-strategy, whose records include one repeated mechanism: Q38/Q87, "PG Unique | goopg Sort under
Gather Merge", at both scales.

## Filed

**M0146-0217**: TPC-DS SF0.25 Q38 (and Q87). PG plans each DISTINCT set-operation leg with a per-worker Unique below
Gather Merge (create_partial_distinct_paths); goopg has no worker-side Unique.
