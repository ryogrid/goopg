# M0146-0186 — the parallel post-pass stands down on a subtree the search costed

Status: closed 2026-10-14.

## Problem

A one-relation query over TPC-DS SF0.25 `date_dim` plans differently:

- goopg: `Gather (cost=0.00..2503.07)`, `Workers Planned: 1`, over a Parallel
  Seq Scan;
- PG 18.3: a plain `Seq Scan (cost=0.00..2519.75)`.

A Gather costed by `cost_gather` starts at `parallel_setup_cost` (1000), so
the 0.00 startup showed it was not a path the cost model had chosen. It was
found while verifying M0146-0005dp (the InitPlan-bounded probe) and appears
for a plain range filter too.

## Diagnosis

The DP trace shows the search handled it correctly:

- the serial scan cost 0..2500.73;
- the partial scan cost 0..2049.55;
- `generate_gather_paths`' Gather cost 1000..3061.25 and was dominated.

The search committed the serial scan. The Gather came afterwards from
`MaybeAddGather` (parallel.go), the post-search pass. It wraps any
partial-capable subtree once `computeParallelWorkers` clears its size rule.
Its "path model wins" coexistence rule stands down only when the plan
already contains a Gather, so a Gather the search had priced and rejected
was put back by the size rule.

## Change

`maybeAddGatherInner` declines a plain target — not a split aggregate, not
a Gather Merge over a Sort — whose single-child spine reaches a node the
join search committed with a costed rel (`spineReachesCostedSearch`: a
`searchRootNode` with `isFromJoinSearch` and a non-empty `Pathlist`). For
such a subtree a serial plan is a lost Gather, which is `add_path`'s
verdict in PG.

Two exceptions keep the pass's old behaviour:

- `debug_parallel_query = on/regress` still forces past the verdict, as PG's
  `standard_planner` adds its Gather regardless of cost. The executor's
  parallel tests use this lever.
- The split-aggregate and Gather Merge targets are separate tournaments and
  keep their own arms.

## Evidence

Files are in `analysis/m0146/m0146-0186/`.

- **Probe:** both forms now plan PG's serial `Seq Scan on date_dim`.
- **Fire set:** no TPC-DS plan moved at either scale (match 75 / 64,
  categories unchanged).
- **SF0.25 sweep:** 99 of 99 pass, no plan change.
- **TPC-H:** the acceptance arm is value-identical, and per-query times
  against the M0146-0180 arm are within noise. No TPC-H plan relied on the
  size rule here.
- **Regress A/B:**
  - `select_parallel`, `write_parallel` and `partition_aggregate` are
    identical.
  - `aggregates` moves by one line in its `balk` case. PG plans Finalize
    Aggregate → Gather → Partial Aggregate over a Parallel Index Only Scan
    (`parallel_setup_cost = 0`). goopg's size-rule Gather had matched part of
    that by coincidence; now the search's serial plan stands.
  - The real gap there is that goopg never splits a user-defined aggregate
    (`AggregateIsDecomposable` refuses `UserAgg`, the deferred M0134-0001
    S10a combinefunc slice).
- **Test:** `TestPostPassStandsDownOnACostedSearchedSubtree` covers three
  cases: a costed searched subtree stays serial, an uncosted one keeps the
  size rule, and `debug_parallel_query` still forces. It fails with the
  stand-down disabled.

## Not ported (ledgered)

- The post-pass itself survives for uncosted subtrees and for its
  split-aggregate and merge arms. Its deletion (C-19h) waits for every
  producer to file costed partial and Gather paths.
- The user-defined-aggregate partial/finalize split (M0134-0001 S10a).
