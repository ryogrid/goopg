# M0146-0101 — inner-unique needs the original GROUP BY

Status: done 2026-10-08 (7c19badd8). Parent: M0146-0100.

## Problem

In TPC-DS Q78 (both scales), PG 18.3 prints `Materialize -> GroupAggregate`
on its merge inner. goopg merged the GroupAggregate bare.

A GroupAggregate cannot mark/restore. `final_cost_mergejoin` therefore
elects `materialize_inner` for it, unless `skip_mark_restore` applies, and
that needs `extra->inner_unique`.

goopg's uniqueness proof, `groupedLeafDistinctFor` (M0146-0005bd), accepted
the grouped subquery as unique when the join clauses equated every
`Aggregate.GroupExprs` entry. `GroupExprs` is the trimmed key list:

- `remove_useless_groupby_columns` (PK functional dependency) drops some
  keys;
- the redundant-pathkey filter (`groupkeyconst.go`, PG's
  `processed_groupClause`) drops keys pinned by a constant.

Q78's `ws` and `cs` subqueries `GROUP BY d_year, item, customer`. The outer
`ss_sold_year = 1998` makes the year join clause a constant restriction, so
`d_year` is pruned and the remaining keys are equated.

## PG behaviour

`rel_is_distinct_for` (analyzejoins.c) reads
`root->simple_rte_array[relid]->subquery`. That is the range table's
original subquery. `set_subquery_pathlist` plans a `copyObject` of it, so
the trimming never reaches it, and `query_is_distinct_for` tests the
ORIGINAL `groupClause`. `d_year` is not in Q78's join clauses (they hold
only item and customer), so the inner is not unique and is materialized.

## Change

- **The Aggregate remembers what was pruned.** It records the input columns
  of the GROUP BY items both prunings dropped, as `PrunedGroupInputs`.
- **The proof requires them.** `groupedLeafDistinctFor` now also calls
  `prunedGroupKeysEquated`, which requires each pruned key to be equated
  through its `Passthrough` output column. The executor appends passthrough
  values at the row's tail, which fixes their output positions.
- **An unread pruned key fails the proof.** It never reaches the output, so
  it cannot be equated — as in PG, where a resjunk group column is never in
  the clause list.

## Verification

- **Probe.** Two grouped subqueries are LEFT JOINed on yr/item/cust, with
  hash join, nested loop and hashagg off:
  - with `WHERE y1 = 1998`, PG and goopg both print Materialize over the
    merge inner;
  - joined on all three keys, both print a bare inner.
  `TestMergeInnerUniqueNeedsPrunedGroupKeys` pins both cases, and dropping
  the new check fails the pinned one.
- **TPC-DS fire set.** Only Q78 changes, and executed results are
  identical. It now differs from PG only in the ws/cs join order and in
  rendering.
- **TPC-H.** Plans are byte-identical between HEAD and the new binary.
- **Regress A/B.** Output is identical apart from join's known row-order
  flap.
- **Gates.** units, TPC-H spotcheck, acceptance arm, fire set, SF0.25
  sweep and ea-ratchet all PASS.
