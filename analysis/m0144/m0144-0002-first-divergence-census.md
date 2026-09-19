# M0144-0002 — First-divergence census (ranked tables)

**Instrument:** `scripts/pg-plan-first-divergence.py` — sibling of
`pg-plan-parity-diff.py`; reuses its parser, N1–N7 normalisation and
category primitives, then aligns the two normalised plan trees node-by-node
in PG's plan-order (pre-order; stream children before SubPlan/InitPlan aux)
and emits ONE mutually-exclusive record per divergent query:
`(parent kind, PG child kind, goopg child kind, depth, category)`.
Design doc: `docs/design/0100-0149/m0144-0002-first-divergence-census.md`.

**Corpora (all committed captures):**

| corpus | goopg | pg | divergent | match |
|---|---|---|---|---|
| TPC-H parallel (canonical, M0144-0001 capture) | `m0144-0001-goopg-parallel.plans.txt` | `m0144-0001-pg-parallel.plans.txt` | 21 | 1 (Q6) |
| TPC-DS SF0.25 | `bench/tpcds/.../plans-20260920-074115.txt` (latest sweep) | `analysis/m0142/m0142-0012verify-tpcds-pg.txt` | 97 | 2 (Q9, Q41 — the floor pair) |
| TPC-DS SF1 | `analysis/m0142/p0e7-tpcds-sf1-goopg.plans.txt` (P0-E7's) | `analysis/m0142/p0e7-tpcds-sf1-pg.plans.txt` | 98 | 1 (Q41) |

Validation: the census's MATCH sets reproduce the parity floor exactly
(TPC-H Q6; SF0.25 Q9+Q41), and Q36/Q70/Q86's `error` records are the three
known `SKIP_QUERYGEN` exclusions (error marker on both sides).

## Ranked first-divergence table

### TPC-H parallel (n=21 divergent)

| n | category | under | PG child | goopg child | depth |
|---|---|---|---|---|---|
| 3 | sort-strategy | (root) | Finalize GroupAggregate | Sort | 0 |
| 3 | sort-strategy | (root) | GroupAggregate | Sort | 0 |
| 2 | aggregation-strategy | Sort | HashAggregate | GroupAggregate | 1 |
| 2 | parallelism | HashAggregate | Gather | (serial child) | 2 |
| 2 | parallelism | (join/agg) | Parallel Hash Join | Hash Join | 3–4 |
| 1+1 | join-method | Partial Aggregate / Sort | Nested Loop | Hash Join / NL Semi→Anti | 3 |
| 1 | qual-placement | Hash Join | Nested Loop | Nested Loop | 2 |
| 1 | parameterisation | Sort | Hash Join | Hash Join | 1 |
| 5 | aggregation-strategy | (root/Sort/HashAggregate) | phased or plain agg | other agg impl | 0–2 |

Category rollup: aggregation-strategy=7, sort-strategy=6, parallelism=4,
join-method=2, parameterisation=1, qual-placement=1.

### TPC-DS SF0.25 (n=97 divergent)

| n | category | under | PG child | goopg child | depth |
|---|---|---|---|---|---|
| 14 | sort-strategy | Limit | GroupAggregate | Sort | 1 |
| 5 | sort-strategy | Limit | Incremental Sort | Sort | 1 |
| 4 | sort-strategy | Limit | Finalize GroupAggregate | Sort | 1 |
| 3+2+2 | sort-strategy | Limit | Sort | CTE ss / ssr | 1 |
| 3 | error | (root) | — | — | 0 (Q36/70/86, both-side SKIP_QUERYGEN) |
| 2+2+2 | aggregation-strategy | Sort / Append / CTE | GroupAggregate / Finalize GroupAggregate | HashAggregate / GroupAggregate | 1–3 |
| 2 | join-method | Nested Loop | Parallel Hash Join | Nested Loop | 8 |
| 2 | join-order | Nested Loop | Parallel Hash Join | Parallel Hash Join (leaf-set) | 6 |
| 2+2 | qual-placement | Nested Loop / Sort | (same kind) | (same kind) | 2 |
| 2 | sort-strategy | WindowAgg | WindowAgg | Sort | 3 |
| … | | | | | (tail: 1-count records, see .txt) |

Category rollup: sort-strategy=43, aggregation-strategy=12, join-method=9,
parallelism=8, join-order=8, qual-placement=8, scan-type=4, error=3,
parameterisation=2.

### TPC-DS SF1 (n=98 divergent)

| n | category | under | PG child | goopg child | depth |
|---|---|---|---|---|---|
| 7 | aggregation-strategy | Sort | GroupAggregate | HashAggregate | 2 |
| 6 | sort-strategy | Limit | GroupAggregate | Sort | 1 |
| 5 | sort-strategy | Limit | Incremental Sort | Sort | 1 |
| 5 | sort-strategy | GroupAggregate | Nested Loop | Sort | 2 |
| 3 | aggregation-strategy | CTE | GroupAggregate | HashAggregate | 2 |
| 3+1 | sort-strategy | Limit | Sort | CTE ss | 1 |
| 3 | error | (root) | — | — | 0 (Q36/70/86) |
| 3 | sort-strategy | Limit | Finalize GroupAggregate | Sort | 1 |
| … | | | | | (tail in .txt) |

Category rollup: sort-strategy=39, aggregation-strategy=26, parallelism=8,
join-order=6, join-method=5, scan-type=5, qual-placement=4, error=3,
parameterisation=2.

## Reading

- **The dominant TPC-DS first-divergence is ordered aggregation under
  `LIMIT`** — `Limit → PG {GroupAggregate | Incremental Sort | Sort+CTE}` vs
  `goopg Sort` at depth 1: ~25 records at SF0.25, ~20 at SF1. PG prefers
  sorted-input aggregation (or Incremental Sort) feeding a top-N; goopg
  reaches for a plain Sort and loses the ordering the downstream
  GroupAggregate/Incremental-Sort wants. This is the same family M0141-S7
  observed losing on cost (3733.01 vs 3730.89) — the census now quantifies
  its blast radius.
- **TPC-H's top class is aggregation-strategy** (7/21): phased-agg
  (`Partial`/`Finalize`) and GroupAggregate-vs-HashAggregate choices at the
  root/Sort boundary — visible only in the parallel corpus; the serial-era
  category tables measured this out of view.
- **parallelism** first-divergences (4 TPC-H, 8 SF0.25, 8 SF1) are mostly
  `PG Parallel … | goopg serial …` pairs — goopg declining the parallel arm
  at the node, not a missing mechanism.
- Deep `Nested Loop under Sort/GroupAggregate` divergences (SF1
  `NL→HJ`/`HJ→NL` swaps at depth 2–8) are the join-order/inner-side
  election class.

Raw per-query records: `m0144-0002-census-{tpch-parallel,tpcds-sf025,tpcds-sf1}.txt`
in this directory.
