# M0146-0003d: the sorted split producer — `Finalize GroupAggregate -> Gather Merge -> Sort -> Partial HashAggregate`

Slice S6 of M0146-0003 (row-emitting PartialAgg), adopting M0141-S6:
planner producer wiring so the row-transport machinery from
M0146-0003b/c is actually elected in production. Until this slice,
`PartialEmit` was dead-in-production — the executor arms existed, the
tests hand-built the shapes, but no path producer filed a transport
split and no createPlan arm could lower one. This is the last slice of
M0146-0003.

## PG behaviour

`gather_grouping_paths` (planner.c:7704-7724) files a presorted partial
grouping path beside the hashed one — both are costed candidates on the
grouped rel, and `add_path`/`set_cheapest` adjudicate:

    Finalize GroupAggregate
      -> Gather Merge
           -> Sort
                -> Partial HashAggregate

Three properties define the shape:

- The Partial is **hashed** (`Partial HashAggregate`) — each worker
  hash-aggregates its claimed share; the Sort re-orders the partial's
  already-narrow output, so the sort sees `partialGroups` rows per
  worker, not `inputRows`.
- The merge keys are **positional** — the transport row is
  `[group key values | passthrough | serialized transition states]`,
  so merge key j names transport column `GroupClause[j].Pos`, and the
  finalized output's position k.Pos is the same column: one key list
  describes the worker sort, the merge, and the ordering the Finalize
  emits.
- The Finalize's sorted combine folds same-key state runs streaming —
  M0146-0003c's `openSortedPartialTransport`, the row-transport twin of
  `openSorted`.

## Change

### Producer — `addPartialAggSortedSplitArm` (partialaggupper.go)

Inside `addPartialAggSplitPath`, gated the same place the hashed split
is: `aggregateSplitIsSafe(aggNode)` (the decomposability gate — a
non-decomposable aggregate has nothing serialisable to transport),
plus `nGroupCols > 0`, `GroupingSets == nil`, `!groupingHasSpecialAgg`,
and `transportGroupSortKeys` succeeding (every group expression must be
a bare `*ColumnRef` so a merge key can name it honestly; anything else
declines the arm only — the hashed split still competes).

The filed path is exactly

    PathFinalizeAgg (AggStrategySorted, Pathkeys = merge keys)
      -> PathGatherMerge (Pathkeys = merge keys)
           -> PathSort (worker Sort over partialGroups rows)
                -> PathAgg (AggStrategyHashed, Partial)

priced like the siblings: `costAgg` per arm, `sortPathForBounded` for
the worker sort, `gatherMergeCost` over `crossed = partialGroups * d`
transported group-states — the shape's whole economic argument (TPC-H
Q1: a few group states per worker against ~1.5 M scanned rows each).
`workerSort.ParallelWorkers` is stamped because `sortPathForBounded`
never plans workers and `gatherChildPlan` refuses a zero-worker child —
R56's rule, verbatim.

The FINAL path's Pathkeys are the transport-position merge keys
themselves: transport position k.Pos IS this path's output position
(the finalize emits group values first, in GroupExprs order), so the
same list is simultaneously the merge order and the ordering the
finalized rows leave in — `add_path`'s usefulness bookkeeping gets one
honest claim, not a translated one.

### Merge keys — `transportGroupSortKeys` (groupclause.go)

Derives `[]SortKey` from `groupClauseKeys(agg)`: clones each group
`*ColumnRef`, rebinds `Index` to the transport position `k.Pos` (the
sort/merge evaluate against transport rows, whose first nGroupCols are
the group values in GroupExprs order), keeps Name/Type/SourceTable for
EXPLAIN's `Sort Key:` rendering, and preserves `Desc`/`NullsFirst` so
the worker Sort, the Gather Merge, and the executor's order belt all
see the same order. Declines on any non-ColumnRef group expression —
fail-closed, the arm simply does not file.

### Constructor — `splitAggregateTransportSorted` (parallel.go)

Builds, in one shot,

    Aggregate{Mode Final, Strategy Sorted, PartialEmit}
      -> GatherMerge
           -> Sort (transport-position keys)
                -> Aggregate{Mode Partial, Strategy Hashed, PartialEmit}
                     -> stampParallelScan(input)

with `final.PartialSource = &partial` (the very node under the Sort),
`final.InputTarget{,Known}` cleared (the final consumes transport rows,
not input rows), and the input subtree stamped via the existing
copy-on-write `stampParallelScan` — plan-cache immutability preserved,
mirroring `splitAggregate`.

### createPlan — `createFinalizeAggSortedPlan` (createplansimple.go)

`createFinalizeAggPlan` dispatches on its boundary child: `PathGather`
lowers as before; `PathGatherMerge` walks
`PathGatherMerge -> PathSort -> PathAgg -> input`, builds the ONE input
subtree, and hands the shape to `splitAggregateTransportSorted`. Every
malformed producer path panics — the same contract the hashed arm
enforces, since a path reaching here in a wrong shape is a producer
bug. The driving scan's display rows get `gatherPathDivisor(gm, srt)`
= `crossed / partialGroups` = d, same rule as the hashed arm.

### Ordering claim — `aggregateEmissionPathkeys` Final arm (upperorderedinput.go)

The upper ORDER BY machinery walks the node tree; an `*Aggregate` is
asked for its emission order via `aggregateEmissionPathkeys`. The new
arm claims ordering only for `Mode Final && PartialEmit &&
Strategy Sorted && GroupingSets == nil && GroupKeyOrder == nil`, and
only when the child is a `*GatherMerge` whose first-nGroupCols keys are
positional `ColumnRef{Index: k.Pos}` in clause order — i.e. it *verifies
the merge contract it claims*. The claim lands in output coordinates
with zero translation (transport position k.Pos is output position
k.Pos), so an ORDER BY on the group keys can be satisfied by the merge
and the top Sort elided — exactly what PG's Q1 does.

### Order belt — `openSortedPartialTransport` (operators_join_agg.go)

M0146-0003c's belt compared with a bare `compareDatum`, which has no
NULL arm and always ascends — it would misorder or error on exactly the
streams a DESC or NULLS FIRST clause makes routine. The belt now
compares in the declared merge order: `partialTransportMergeOrder`
replicates `groupClauseKeys` (honour the clause when it is a complete
permutation of the group expressions, else written-order ASC NULLS
LAST) and each position is compared through
`compareDatumWithNullsFirst` with the clause's `Desc`/`NullsFirst` —
the same order the worker Sort and Gather Merge actually produced. A
key below the just-emitted group still errors XX000 rather than emit a
group twice.

### StripGather fold (parallel.go)

The cache-side fold now restores `c.Strategy = src.Strategy` — the
sorted split stamps the Final's strategy `AggStrategySorted`; folding
it back to `Simple+Sorted` over the stripped (unordered) input would
let `openSorted` trust an order the scan no longer provides — a
silently-wrong order claim. The Partial keeps the stamped spec's own
strategy, so `src.Strategy` is the honest revert.

## Evidence

- `TestUpperSplitSortedTransportArmFilesThePGShape` — the candidate is
  filed with exactly `PathFinalizeAgg(Sorted) -> PathGatherMerge ->
  PathSort -> PathAgg(Hashed)`, survives `add_path`, and prices
  positive on every leg.
- `TestUpperSplitSortedTransportArmRefusals` — non-ColumnRef group expr
  declines only the arm; a DISTINCT aggregate declines the split
  family.
- `TestUpperSplitSortedTransportLowering` — `createPlanNode` emits
  `Finalize(Sorted, PartialEmit, PartialSource) -> GatherMerge -> Sort
  -> Partial(Hashed, PartialEmit)` with positional keys and a stamped
  parallel scan.
- `TestStripGatherFoldsTheSortedSplitBack` — the fold reverts to
  `Simple + Hashed`, un-stamps the scan, drops `PartialSource`.
- `TestPartialEmitSortedHonoursClauseOrder` — the belt honours DESC and
  explicit NULLS FIRST/LAST, accepts NULL-key runs (folded into one
  group), and still rejects a descending-under-ASC stream.
- Pre-existing pins updated for the new dominance: the sorted split can
  legitimately evict the hashed split and the no-split sorted arms —
  equal-or-better cost plus merge-order pathkeys is strict dominance in
  `add_path`, the same thing PG's add_path does to its own arm pair.
- Fire-set (private clones, both TPC-DS scales): the arm elects on 11
  queries per scale — e.g. TPC-DS Q42 now renders
  `Finalize GroupAggregate -> Gather Merge -> Sort -> Partial
  HashAggregate`; every moved plan still executes with correct values
  (all fire executions PASS, `introduced=none`).

## Deferrals

- A group expression that is not a bare `*ColumnRef` declines the arm
  (ledgered) — transport-position merge keys cannot be named/rendered
  honestly for an arbitrary expression.
- `GROUPING SETS`, aggregate `ORDER BY`/`DISTINCT`/`WITHIN GROUP`, and
  the non-decomposable families keep their existing fail-closed
  behaviour (the arm never files; hashed/gathered arms unaffected).
