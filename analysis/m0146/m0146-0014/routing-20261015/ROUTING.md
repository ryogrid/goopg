# M0146-0014 — S9 census pass at a00fb8110 (2026-10-15)

Two corpora changed since `routing-20261014j` (exhausted at `c3dcb0f5f`):

- the TPC-DS SF0.25 reload of 2026-10-15 (`285df1201`);
- the TPC-H realign of 2026-10-15 (`8a149f81d`).

The captures reused here were taken after those changes. No commit since has
touched `internal/optimizer`, `internal/planner`, `internal/executor` or a cost,
stat or selfuncs path:

| corpus | capture | match |
|---|---|---|
| TPC-DS SF0.25 | `analysis/m0146/m0146-0178/slice2/head-*` (clean-HEAD `d7c05acde`) | 80 / 99 |
| TPC-H | `routing-realign-20261015` (`8a149f81d`) | 12 / 22 |
| TPC-DS SF1 | `routing-20261014j` arm, still fresh: SF1 reloaded 2026-10-13, before it | 66 / 99 |

First-divergence records come from `scripts/pg-plan-first-divergence.py`
(`first-divergence-*.txt`). Records new since 14j were routed against live
tasks.

| corpus | query | category | route |
|---|---|---|---|
| SF0.25 | Q56, Q60 | sort-strategy (PG Merge Append, goopg Sort) | M0141-S2b-4e (live) |
| SF0.25 | Q23 | scan-type | actionable |
| SF0.25 | Q33 | sort-strategy | actionable |
| SF0.25 | Q43 | join-method | actionable |
| TPC-H | Q2 | join-method | actionable |
| TPC-H | Q3 | join-method | actionable |
| TPC-H | Q7 | join-method | actionable |
| TPC-H | Q5 | scan-type | actionable |
| TPC-H | Q18 | parallelism | actionable |

Actionable totals: join-method 4, scan-type 2, sort-strategy 1, parallelism 1.

**Filed: M0146-0195** (join-method).

Q3 and Q7 (and Q5's scan-type record) share one divergence: PG probes lineitem
through `idx_lineitem_orderkey_fkidx` in a parameterised Nested Loop, and goopg
never does.

| query | PG | goopg |
|---|---|---|
| Q7 | probe at 1.20 | Parallel Hash Join over a Parallel Seq Scan |
| Q5 | probe at 3.31 | `lineitem_pk` probe at 7.77 |

goopg's `order_customer_fkidx` probes also price about 0.65 above PG's
(2.21 vs 1.56, 2.29 vs 1.64).
