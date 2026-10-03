# M0146-0005du — min()/max() keeps the Aggregate when no index can order the column

Status: done (2026-10-04, `e625c333c`). Parent: M0146-0005.

## PG behaviour

`preprocess_minmax_aggregates` (`./postgres/src/backend/optimizer/plan/planagg.c`)
does not rewrite unconditionally. For each min/max aggregate,
`build_minmax_path` plans `SELECT col FROM … WHERE col IS NOT NULL ORDER BY
col LIMIT 1` and then asks
`get_cheapest_fractional_path_for_pathkeys(final_rel->pathlist,
query_pathkeys, …)` for a path that is already sorted (planagg.c:443-448).
No explicit Sort is added. Without such a path it returns false, no
MinMaxAgg path is added, and the plain Aggregate stands.

## goopg before

`rewriteMinMaxAggregates` (planner.go) built the InitPlan whenever the
syntactic gates passed. With no index it fell back to
`Limit → Sort → Seq Scan`, a plan PG never builds.

## Change

`minmaxPresortedIndexExists` answers PG's question for the statement's
WHERE: does some non-partial btree index put the column next in its order
once the columns before it are bound by `column = constant` conjuncts?
Other conjuncts may sit beside the equalities. When it answers no, the
rewrite declines and the Aggregate stands.

## Verified against PG 18.3

| query | PG | goopg before | goopg now |
|---|---|---|---|
| `min(x)` / `max(x)`, no index | Aggregate → Seq Scan | InitPlan Limit → Sort → Seq Scan | Aggregate → Seq Scan |
| `min(y)` over index (x, y), `WHERE z = 5` | Aggregate | Sort fallback | Aggregate |
| `min(y)` over index (x, y), no WHERE | Aggregate | Sort fallback | Aggregate |
| `min(y)` over index (x, y), `WHERE x = 33 AND y < 500` | InitPlan → Limit → Index Only Scan | Sort fallback | Sort fallback (ledgered) |

The tests that pinned the old fallback were re-checked against PG and
changed to PG's answer. Two EXPLAIN tests expected the rewrite's nested
InitPlan numbering on unindexed tables, where PG prints `(InitPlan 1)`. The
InitPlan-indent and sub-plan-stats tests now create the index their shape
needs; the indent test then reproduces `aggregates.out:939-947` exactly.

No TPC-DS or TPC-H query takes min/max over an unindexed column, so the
fire set saw no change.

## Not done (ledgered)

- An index that orders the column but cannot serve goopg's index-only
  probe (for example a range conjunct on the column) still takes the Sort
  fallback, where PG takes the index path.
- PG compares the MinMaxAgg path's cost with the Aggregate's
  (`add_path` on the grouped rel). goopg rewrites whenever a presorted
  index exists.
- PG drops a constant GROUP BY key and the ORDER BY over it. goopg keeps
  both, as `TestExplainSortKeySubqueryGroupByUnchanged` shows.
