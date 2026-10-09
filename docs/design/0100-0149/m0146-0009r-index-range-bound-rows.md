# M0146-0009r — an index scan's range bounds are scored by the histogram

Status: done (2026-10-05). Parent: M0146-0009. Filed by M0146-0006.

## The divergence

Regress `aggregates`' `agg_sort_order` has 100 rows, `c1` as its primary key
and `c2` unique. PG 18.3 estimates `WHERE c2 < 100` at 99 rows and `c2 < 50`
at 48, from the histogram (`scalarineqsel`). It does so on every path.

goopg read 33 for both bounds whenever the restriction sat on an index range
scan:

- **Default settings.** The single-table rule-based index producer
  (`planIndexScanFromWhere`) printed `Index Scan … rows=33`.
- **`enable_seqscan = off`.** The c2 index range scan was the aggregate's
  input during the search. The grouped rel was sized at 33 groups (PG 99).
  That made `cost_incremental_sort` see 33 presorted groups and elect an
  Incremental Sort over the pkey scan. This is the regression M0146-0006
  measured.

## Cause

`EstimateRows` for an `IndexScan` / `IndexOnlyScan` is `indexScanRows`
(`cardinality.go`). That function charged a flat `DEFAULT_INEQ_SEL` per range
bound. PG's `btcostestimate` instead scores the index quals with
`clauselist_selectivity` (`./postgres/src/backend/utils/adt/selfuncs.c`,
`./postgres/src/backend/optimizer/path/clausesel.c`). That is the
histogram's `scalarineqsel`, and a two-sided range pairs its bounds into
one band. The search's own index path already did this
(`rangeIndexSelectivity`), so EXPLAIN's row stamp and the node estimate
disagreed.

## Change

`indexScanRows` takes the bounds' operators (`LowOp` / `HighOp`; the zero
value is inclusive). `indexRangeBoundSelectivity` builds the bound conjuncts
(`col >= low` or `col > low`; `col <= high` or `col < high`) on the index
column after the equality prefix. It scores them with
`conjunctionSelectivity` over a seq scan of the table. That reuses the Filter
estimator's histogram, MCVs and range pairing. When the column cannot be
named, `DEFAULT_INEQ_SEL` per bound stays the fallback.

## Effect

- **`agg_sort_order`.**
  - Default settings now read PG's 99 and 48.
  - With seq scans off, the plan is PG's: Sort over GroupAggregate (rows
    99) over Sort over the c2 Index Scan. The regress hunk is gone (5 → 0
    lines naming it).
- **Fire set.** No query fired at either scale; the sweep passed 96/96 and
  the TPC-H arm 24/24; tpch-spotcheck and ea-ratchet passed.
- **Regress A/B** over 14 cases:
  - `aggregates` lost the `agg_sort_order` hunk;
  - `subselect`'s `tattle` NOTICE order and a known `join` row-order flip
    differ run to run.

Test: `TestRangeRestrictionRowsOnEveryPath` fails on HEAD.

## What was tried and backed out

Restricting the rule-based producer to correlated scopes would also let the
default-settings plan keep PG's Seq Scan. That rule exists for correlated
restrictions (TPC-H Q17/Q20), but it replaces the search's costed seq scan
for any WHERE. Gating it on a correlated reference broke 11 unit tests that
rely on the rule for constant keys. That is too wide for this slice. It is
filed as M0146-0060.

## Not done (ledgered)

- M0146-0060, above.
- A volatile function in a grouped subquery's target runs three times per
  row in goopg and once in PG. In regress `subselect`, `tattle()` emitted 9
  NOTICEs against PG's 3. This is pre-existing; the results agree.
