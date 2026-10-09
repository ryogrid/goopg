# M0146-0009i — Q14's CTE Scan row estimate: already resolved, verified

Status: done (2026-10-04, verification only; fixed by M0145-0008ac,
`c317b037b`). Parent: M0146-0009.

## The task

Filed 2026-10-02 by the slice-114 routing census. In TPC-DS Q14, the
`cross_items` CTE was estimated at rows=1 on both engines. goopg's CTE Scan
of it reported 212, though, so goopg hashed the CTE where PG unique-ifies
its one row into an index-probe chain.

## What the current plans show

The fire set of `216da71d5` (2026-10-04) has these Q14 plans at both scales:

- Every `CTE Scan on cross_items` reports rows=1, as in PG. No node in
  goopg's Q14 plan reports 212 rows.
- Each `ss_item_sk IN (SELECT … FROM cross_items)` branch now has PG's
  shape: a `HashAggregate` (Group Key ss\_item\_sk) over the one-row CTE Scan
  drives the nested-loop probe chain. That makes 10 such unique-ify nodes
  at SF1 on both engines.

## Why it changed

The M0145-0008z recon listed Q14's five `IN (SELECT … FROM cross_items)`
sublinks as declined for pull-up, reason
`any-body-leaf-(*optimizer.CTEScan)`. The sublink therefore stayed a
subplan, and its CTE scan was sized outside the join search.

M0145-0008ac (`c317b037b`, 2026-10-03) admits a CTE Scan as a base rel of
the pulled-up semi join, as PG's `pull_up_sublinks` does. The CTE Scan
is then sized from the CTE subplan's rows (`set_cte_pathlist`) and
unique-ified like any other semi-join inner.

## What is left in Q14

At SF0.25 Q14 still differs in join-order, scan-type, parameterisation and
qual-placement; SF1 adds parallelism. These come from the probe choice below
the unique-ified CTE: goopg picks a bitmap probe where PG picks an index
probe. That choice is held on the owner-parked index-probe cost multiplier
(M0145-0008ag), not on this estimate. The `Group Key` qualifier
(`cross_items.ss_item_sk` in PG) is a rendering difference.
