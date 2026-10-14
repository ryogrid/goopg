# M0146-0194 — the Q72 record is a near-tie among PG's own candidates

Record: TPC-DS SF0.25 Q72, `depth=3 [sort-strategy] under GroupAggregate: PG Nested Loop Left | goopg Sort`.

## PG side (tmp/pgdbg, db tpcds025)

| setting | plan | cost |
|---|---|---|
| default | GroupAggregate over a Nested Loop Left fed by a Gather Merge sorted on the group keys (PG's chosen shape) | 15483.78 |
| `enable_gathermerge = off` | Finalize GroupAggregate → Sort → Gather → Partial GroupAggregate, all joins in the workers | 15469.42 |

The second plan is cheaper in total by 0.09%, well inside STD_FUZZ_FACTOR. It is worse in startup (15469.35 vs
15468.82 at the grouped rel), so add_path's tight-fuzz comparison calls the pair COSTS_DIFFERENT and keeps the
incumbent. PG's choice among its own candidates therefore comes down to an add_path tie-break, not to a
cost-term difference.

## goopg side

goopg's plan (15196.08) gathers lower and joins warehouse, item, d3 and promotion serially. Its whole cost level
sits about 290 below PG's, and the gap is made of known residual terms:

- the catalog_sales Parallel Seq Scan costs 10521.57 vs 10578.57 (relpages, M0146-0009h residual);
- the d2 probe sits under a Memoize, where PG has a plain index scan with a Join Filter;
- inventory's index scan costs 100.52 vs 101.89.

goopg's election among shapes this close is the same kind of tie. The record is COSTTIE class, not a missing
mechanism.

## Side observation (ledgered, not the record's cause)

goopg prices the d3 probe `Index Scan using date_dim_pkey on date_dim d3` at 0.29..8.31 where PG prices it at
0.29..0.33. PG's `get_loop_count` takes the loop count from the parameter's source rel (catalog_sales), so the
index pages are mostly cached. goopg's figure looks like a loop count of 1.
