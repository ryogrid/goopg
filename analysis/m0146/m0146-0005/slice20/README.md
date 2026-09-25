# M0146-0005 slice 20 (M0146-0005t): EXPLAIN names a column by its own level

Q14's depth-7 record (SF0.25, `join-order` under Nested Loop) was not a
plan difference. The plans match; goopg printed the cross_items join as
`Hash Cond: (store_sales.ss_sold_date_sk = date_dim.d_date_sk)` where PG
prints `(store_sales_1.ss_sold_date_sk = d1.d_date_sk)`. The census keeps
an alias qualifier (`d1`) and drops a table-name one, so the conditions
did not match.

## Causes

1. `createSeqScanPlan` rebuilt a search-chosen seq scan without the leaf's
   `RTID`, which the index and bitmap arms carry. Those scans never
   registered in EXPLAIN's range-table name table.
2. `explainNames.column` resolves a column through one statement-wide
   SourceTableIdx map. The planner restarts SourceTableIdx at 1 in every
   query level, so a CTE body's columns resolved to the outer query's
   relations.

## Change

- `createSeqScanPlan` carries `RTID`.
- `explainNames.columnIn` resolves a column against the scans under the
  node being rendered, then under its ancestors (a parameterised inner
  scan names its outer side from the join above), stopping at a CTE or
  set-operation boundary. It is PG's set_deparse_plan namespace. Both
  EXPLAIN walkers set `subPlanReg.current`.
- Scan labels print ExplainTargetRel's `<relation> <refname>` when the
  range-table name differs (`Seq Scan on tenk1 tenk1_1`, not
  `Seq Scan on tenk1_1`). The stamped RTIDs made those labels common.

## Results

- The cross_items join now reads `(store_sales.ss_sold_date_sk =
  d1.d_date_sk)`. Q14's SF0.25 record moves past the whole cross_items
  subtree to `CTE avg_sales` (PG plans it as a top-level CTE; goopg
  inlines it under an InitPlan).
- `rendering` records: SF0.25 27 -> 25, SF1 28 -> 26 (fire-set
  CATEGORIES-EXCL-MATCH). SF1 first-divergence census unchanged.
- Row counts unchanged (sweep 96/96, fire set PASS); TPC-H census
  identical; regress: no case worse, `union` 25 lines closer.
