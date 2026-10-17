# M0146-0180 — a partial aggregate over UNION ALL stands on the Parallel Append

Status: closed 2026-10-14.

## Problem

TPC-DS Q76 groups a three-branch UNION ALL. Both engines plan the same
shape: Finalize GroupAggregate, Gather Merge, Sort, Partial HashAggregate,
then a Parallel Append over the three branches. The costs differed at SF0.25:

- PG 18.3 printed `Parallel Append (cost=0.58..37614.25)`. That is
  `cost_append`'s partial arm: the sum of the children's totals plus
  `cpu_tuple_cost * 0.5 * rows`.
- goopg printed `Parallel Append (cost=1000.58..14112.00)`, below its own
  first child (17943). The query totals were 15612 against PG's 39026.

## Diagnosis

The Append path was priced correctly. The set-operation search's partial
path (`upper.setop.append.partial`) cost 37396 for the whole chain, close to
PG's figure.

A stack captured at the node's cost stamp showed that the printed cost was
the partial-aggregate split's input path:

1. `createGroupingPaths` hands the split the committed input, here the set
   operation's serial winner: a Gather over the SetOp, startup 1000.58.
2. `searchedCheapestPartialInput` should replace it with the input rel's
   `cheapest_partial_path`, as `create_partial_grouping_paths` does
   (planner.c). It answered only for a join search's rel, and returned nil
   for a set operation.
3. With no partial path, `addPartialAggSplitPath` seeds the input with
   `parallelSeedCost`. That keeps the serial startup, including the
   Gather's `parallel_setup_cost`, and divides the run cost by the parallel
   divisor: about 1000.58 + (42684 − 1000.58) / 3.1 ≈ 14450. The printed
   figure moved between 14112 and 14198 with the ANALYZE sample.

PG has no such path. Its UNION ALL is an appendrel, and the appendrel's
`cheapest_partial_path` is the Parallel Append that
`add_paths_to_append_rel` built.

## Change

`searchedCheapestPartialInput` (searchedtree.go) also answers for a node
tagged with its SETOP rel (`setOpBranchRelNode`: a SetOp, Gather or
GatherMerge that `createSetOpPaths` returned):

- It reads that rel's `PartialPathlist[0]`.
- It builds a Gather path over it and the node from that path, so the split
  unwraps the Gather exactly as for a join rel.
- No boundary rebuild is needed: the set-op node is built straight from its
  path, in the same column order as the committed one.

The split then takes the partial path's own workers, per-worker rows and
cost, as it already did for a join input.

## Evidence

Files are in `analysis/m0146/m0146-0180/`.

- **Q76 at SF0.25:** Parallel Append 0.58..37552.80 (PG 0.58..37614.25). The
  query total is 38969 against PG's 39026 (was 15612). Values equal PG's.
  - It remains a shape diff, as before: the catalog_sales branch is a
    Parallel Hash Join where PG nests loops into `item_pkey`. The branch's
    row estimate is 582 against PG's 509, and the two costs are within 20.
- **A three-table UNION ALL GROUP BY:** goopg now plans PG's
  Finalize, Gather Merge, Sort, Partial HashAggregate, Parallel Append shape.
  HEAD planned a serial HashAggregate over a plain Append.
- **TPC-DS fire set:**
  - SF1 match 63 → 64: Q14 now matches PG.
  - SF0.25 match stays at 76. Q14 and Q76 fire at both scales; their results
    are unchanged.
- **TPC-H:** the acceptance arm is value-identical.
- **Regress A/B:** `union`, `select_parallel`, `partition_aggregate` and
  `aggregates` are identical.
- **Test:** `TestExplainPartialAggOverUnionAll` fails with the new arm
  disabled. It loses all four PG plan lines.

## Not ported (filed)

- **M0146-0182.** The split chain's Gather Merge prints the Sort's startup,
  without `parallel_setup_cost`, and the partial group count as its rows
  (Q76: 37913.74, rows 3443; PG: 39026.66, rows 10044). The Finalize above
  it is priced from the path, so the gap is display-only.
- **Nested set-operation chains.** goopg's binary SetOp chain prices a
  three-branch UNION ALL link by link, while PG flattens it into one
  appendrel. On the probe above, goopg's Parallel Append costs 12853 where
  PG's `append_nonpartial_cost` gives 8020. The chain rule is M0144-0003b-1's
  territory.
- **M0146-0183.** Q5's two-key Hash Right Join is costed about 4049 above its
  inputs, where PG adds about 945.
