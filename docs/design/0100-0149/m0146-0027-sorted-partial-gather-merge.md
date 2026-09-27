# M0146-0027: parallel partial-subtree reach — sorted-partial Gather Merge + searched-candidate consumption

Status: slice 1 landed 2026-09-27 (ORDER BY / GROUP BY stages); slice 2
landed 2026-09-27 (the partial-DISTINCT producer — `Unique -> Gather
Merge -> Unique`, §7); slice 3 landed 2026-09-27 (the sorted-input
partial arm — `Partial GroupAggregate -> Sort`, §8). Remaining: the
Gather-Merge placement residue, see §6; the Q62/Q99 epsilon-adjudication
residual, §8's tail.

## PG behaviour

`generate_useful_gather_paths` (allpaths.c:3235) has two halves. The first
goopg already had: a bare `Gather`/`Gather Merge` over the cheapest partial
path. The second (allpaths.c:3255-3341) — "consider sorted paths for each
interesting ordering" — was deferred (the C-19e design's explicit
deferral): for every useful ordering `get_useful_pathkeys_for_relation`
returns, PG walks the partial pathlist and, where `pathkeys_count_
contained_in` shows the subpath does not already deliver the ordering,
files `create_sort_path(root, rel, subpath, useful_pathkeys, -1)` inside
the workers and a Gather Merge over it:

    Gather Merge
      -> Sort                      priced per-worker (create_sort_path
         -> <partial path>            carries subpath->parallel_workers)

TPC-DS Q17's oracle shows why this matters: `GroupAggregate -> NL -> NL ->
Gather Merge -> Sort -> <5-rel partial NL chain>` — the ordering survives
through the serial NLs above (outer-pathkey propagation) and feeds the
sorted GroupAggregate with no top-level Sort.

Two further upstream mechanisms let that ordering reach an upper stage:

- `create_ordered_paths` (planner.c:5342-5345) iterates the WHOLE
  `input_rel->pathlist` and offers every path that already satisfies the
  ordering directly (the `is_sorted` arm) — not only cheapest-total.
- `add_paths_to_grouping_rel` (planner.c:7134) does the same for the
  GROUP BY stage's sorted-input requirement.

## Why goopg could not produce it

Three stacked gaps, each verified by DPPATH on the private SF0.25 clone:

1. `generateUsefulGatherPaths` stopped at the first half — no
   `Gather Merge -> Sort -> partial` candidate was ever filed, and
   `findPartialSubtree`'s post-pass stood down entirely once the path
   model had elected any Gather (`parallel.go:191`'s `subtreeHasGather`).
2. `addOrderedPaths` and `addGroupingPaths` each saw only the searched
   rel's cheapest-total seed. The M0141-S2b plumbing
   (`SearchCandidates`/`SearchCandidateKeys`) existed but had no consumer,
   so an ordering-carrying runner-up could never reach the ordered or
   grouping contest.
3. `aggregateEmissionPathkeys` recognized `Sort`/`GatherMerge` children
   only; once a searched-root child won, the ORDER BY stage stacked a
   redundant top Sort.

## Change

`internal/optimizer/`:

- **gatherpaths.go** — the sorted-partial arm. For each `useful` in
  `usefulPathkeysForRelation(rel)` (the query-pathkeys prefix whose every
  key passes `pathkeySortableEarly` — `relation_can_be_sorted_early`'s
  reltarget-membership test via `searchCtx.itemSpans` plus
  `isParallelSafeExpr`), walk `rel.PartialPathlist`: skip subpaths whose
  `pathkeysCountContainedIn(sub.Pathkeys, useful)` is already contained
  (the first half filed the bare Gather Merge over those); for the rest
  file `makeGatherMergePath(rel, sortPathForBounded(sub, useful, s.cp, -1))`
  labelled `gather.merge.sort`. Non-cheapest subpaths are gated the
  upstream way: only reachable when `presorted > 0` AND incremental sort
  is on — and that arm stays deferred to M0146-0006, so on this build
  effectively only the cheapest partial is sorted, matching upstream.
  `sortPathForBounded` now propagates `sub.ParallelWorkers`
  (`create_sort_path` copies `subpath->parallel_workers`); goopg's
  partial paths carry per-worker rows so the sort is priced exactly as
  upstream's worker-side sort.
- **joinsearch.go / relfromjoinlist.go** — `searchCtx.itemSpans` carries
  the joinlist item-coordinate windows so `pathkeySortableEarly` can
  resolve a pathkey expression's rels.
- **joinpathsmerge.go** — `partialPathDrivingKind` admits `PathSort`:
  `attachParallelScan`'s `sortOp` arm already descends through it.
- **upperordered.go** — `addOrderedPaths`' `is_sorted` arm: for every
  `SearchCandidates[i]` whose `SearchCandidateKeys[i]` contains the ORDER
  BY ordering, rebuild through `searchedCandidateInput` and offer the
  candidate directly (`upper.ordered.searchcand`). Containment is tested
  on the re-validated keys, never on the candidate's own search-space
  claim (the same rule `stampSearchPathkeys` applies to the winner).
- **groupingpaths.go** — `upper.groupagg.searchcand`: the same iteration
  at the grouping stage; qualifying candidates are rebuilt and wrapped in
  a sorted `PathAgg`. `addPath`/`setCheapest` adjudicate — no forcing.
- **searchedtree.go** — `searchedCandidateInput(n, p)`: the
  `searchedCheapestTotalInput` walk parameterized on the path. Rebuilds
  the candidate through the committed boundary (`createPlanAtSearchRoot
  Range`), re-wraps pass-through Project/Sort nodes, verifies the rebuilt
  output schema equals the committed one. `searchedBoundaryRebuild`
  replays `p.Rel.BoundaryFill` — the hole-filler closure the committed
  publication ran under (stamped in createplanroot.go) — so a candidate
  whose narrowed leaf dropped a below-only column is judged by the same
  license; holes the filler declines still fail closed, and an internal
  boundary panic is recovered into a decline (DPPATH-logged), never a
  planner crash.
- **upperorderedinput.go** — `aggregateEmissionPathkeys`' sorted arm
  accepts a searched-tree child carrying validated `searchedPathkeys`,
  in the aggregate's output coordinate space, so no redundant top Sort.

## Correctness argument

Every offered candidate is re-earned against the searched rel's published
schema (`validatedSearchCandidateKeys`) and reconstructed through the same
boundary machinery the winner used (`searchedCandidateInput` →
`createPlanAtSearchRootRange` with the rel's own `BoundaryFill`). A
candidate that cannot reproduce the committed output row shape is
declined, not elected; `addPath` still prices it normally. Worker safety
is unchanged: the Sort runs per-worker under Gather Merge, the executor's
existing `sortOp` descent attaches the parallel scan below it, and
per-worker row conventions are preserved end to end.

## Evidence

`analysis/m0146/m0146-0027/` — SF0.25 census at HEAD vs baseline
(32d779e41): divergent 89 → 88, matches 10 → 11, sort-strategy records
43 → 38. Q7 → MATCH. Q17/Q25/Q29 pushed from `Sort under GroupAggregate`
to a deeper `Gather Merge` placement record; Q50 to qual-placement; Q77
to scan-type. Q17 emits PG's spine (`q17-pg-sf025.txt` vs
`q17-goopg-sf025.txt`). Sweep PASS=96/0 mismatches; TPC-H spotcheck PASS;
units green. Tests: `TestGenerateUsefulGatherPathsSortedPartialArm`,
parallel-off negative in upperorderedinput_test, PathSort descent in
joinpathspartialmerge_test.

## Slice 2 (2026-09-27): the partial-DISTINCT producer

The `Unique`-head records turned out NOT to need the `is_sorted` consumer
after all: PostgreSQL never routes DISTINCT through one. Upstream files a
whole `UPPERREL_PARTIAL_DISTINCT` stage — `create_partial_distinct_paths`
(planner.c:4852) plans per-worker `Unique` over sorted partial inputs,
gathers them ordered, and `create_final_distinct_paths` (planner.c:5167)
stacks a leader-side `Unique` on the merge:

    Unique
      -> Gather Merge
           -> Unique               per-worker dedup
                -> Sort
                     -> <partial subtree>

goopg had no counterpart stage — `createDistinctPaths` offered only the
final hashed and sorted candidates over a serial seed — so Q38/Q87 could
never reach the shape regardless of ordering machinery.

**Change** (`internal/optimizer/`, `internal/executor/`):

- **distinctpaths.go — `addPartialDistinctPaths`**: the producer, built
  node-level on the M0146-0025 partial-Group arm's model (the partial
  input is the serial subtree run once per worker; a search-placed
  `Gather` under it is unwrapped by `gatherToUnwrapForPartialAgg` or
  spliced by `spliceGatherOnPartialSpine`, never discarded). Per-worker
  `Sort` on the dedup key list priced at per-worker scale
  (`sortPathForBounded` + `ParallelWorkers`), a marked worker `Unique`
  priced at `estimate_num_groups` over the per-worker rows
  (planner.c:4897-4900's numDistinctRows), `Gather Merge` crossing
  `partialGroups × d` rows — the whole economic argument of the shape —
  and a final unmarked `Unique`. Deliberately narrower than upstream:
  the partial HASHED-distinct arm (planner.c:4989) and the empty-keys
  LIMIT-1 arm are declined, not approximated — goopg's hashed partial
  emission is transition-state transport a dedup cannot feed.
- **plan.go — `PartialUnique` on `Distinct` (spec) and `DistinctOn`
  (node)**: the `PartialGroup` marker's counterpart. A marked node
  promises a leader-side Unique re-dedups the merged worker streams, so
  partitioning its input is the intended semantics; an unmarked dedup
  node stays a traversal wall in every walk below. `createDistinctPlan`
  propagates spec → node.
- **parallel.go** — marker-gated `*DistinctOn` arms in all four spine
  walks (`stampParallelScan`, `drivingScan`, `drivingScanCrossesSort`,
  `unstampParallelScan`; `perWorkerDisplayRows` is covered transitively
  through `drivingScans`).
- **executor/parallel_scan.go** — the same marked-only descent in
  `attachParallelScan`, `attachParallelIndexScan`,
  `attachParallelBitmapScan`. Without an arm a partial Unique would have
  each worker run the whole child and emit every cross-partition
  duplicate N times — the over-count the marker exists to prevent.

**Evidence**: Q38 and Q87 both emit the PG spine (`q38-goopg-plan.txt`,
`q87-goopg-plan.txt` — three `Unique` heads under the INTERSECT's
HashSetOps all parallelized) with oracle-equal checksums
(`77188220d949e451`, `daa38faef432c025`); sweep plan channel: changed =
Q38/Q54/Q87, all clause-level DISTINCT. Tests in distinctpaths_test.go
(`TestPartialDistinctArm*` shape/lowering/unwrap/refusal,
`TestPartialDistinctWalkAgreement`) plus executor identity at 1/2/4
workers (`TestPartialUniqueGatherMergeIdentity` — the per-worker dedup +
leader dedup collapses cross-worker duplicates exactly once).

## Slice 3 (2026-09-27): the sorted-input partial arm

Upstream's `create_partial_grouping_paths` (planner.c:7518-7560) files a
second aggregation family beside the hashed one this task's earlier
slices built: for each useful partial ordering it sorts the *input* by
the group keys and stacks a sorted partial aggregate on top —
`create_agg_path(… AGG_SORTED, AGGSPLIT_INITIAL_SERIAL …)` — so the
partial streams one serialized state row per group boundary instead of
draining a hash table. The presorted `Sort -> Partial HashAggregate`
wrap (M0146-0003d's arm, upstream's `gather_grouping_paths` sort loop)
files *after* it, which makes filing order the tie-breaker when the two
land within `comparePathCostsFuzzily`'s tight epsilon — and upstream's
own `cost_agg` comment makes the contest honest: "AGG_SORTED and
AGG_HASHED have exactly the same total CPU cost" in the no-spill model,
so election rides on the sort-volume delta (input rows vs emitted
groups), the lower sorted-startup, and the hash spill arm at tiny
work_mem. The shape PG elected on TPC-DS Q19/Q62/Q99:

    Finalize GroupAggregate
      -> Gather Merge
           -> Partial GroupAggregate   sorted partial, serialized emit
                -> Sort                group keys, input space
                     -> <partial subtree>

**Change** (`internal/optimizer/`, `internal/executor/`):

- **partialaggupper.go — `addPartialAggSortedInputArm`** (producer tag
  `upper.groupagg.sortinput`), called in `addPartialAggSplitPath`
  immediately before `addPartialAggSortedSplitArm` to match upstream's
  file order. Refuses fail-closed on grouping sets, special aggregates,
  empty group keys, or group keys `transportGroupSortKeys` cannot
  express as merge keys. Builds `PathAgg{Sorted, PartialEmit} ->
  PathSort{input-space keys, per-worker rows, ParallelWorkers stamped}
  -> pseed` under a `PathGatherMerge` whose pathkeys carry through the
  partial's emitted order, then the existing `PathFinalizeAgg{Sorted}`
  and normal `addPath` pricing/pruning — no forcing.
- **createplansimple.go — `createFinalizeAggSortedPlan`** dispatches on
  the GatherMerge child's `Kind`: `PathSort` is the presorted arm,
  `PathAgg` the sorted-input arm (its own child is the worker Sort).
- **parallel.go — `splitAggregateTransportSortedInput`** clones the
  spec into `Finalize{Sorted, PartialEmit}` over a `GatherMerge` over
  `Partial{Sorted, PartialEmit}` over `Sort{inputKeys}` over the stamped
  input. `StripGather` folds it for free: `Simple+Sorted -> Sort ->
  input` is a valid serial plan via the pre-existing `*Sort` arm in
  `unstampParallelScan`.
- **executor/operators_join_agg.go — `openSortedPartialEmit`** is the
  streaming sibling of `emitPartialStateRows`: `sameGroupKey` boundary
  detection over the ordered input, one `[keys | passthrough |
  serialized state]` row per group, sharing the existing sorted
  transport wire shape and the final's order belt; a `PartialEmit`
  sorted node without the emit path falls through to the hash drain.
  Dispatch in `Open` keys on `Mode=Partial && PartialEmit &&
  Strategy=Sorted && no grouping sets && GroupExprs>0`.

**Election mechanics** (pinned by `TestUpperSplitSortedInputArmElection`
over direct arm calls): near-saturated groups tie within tight fuzz —
sorted-input survives on filing order + lower startup; large reduction
correctly elects the presorted sibling (sorting emitted groups is
materially cheaper than sorting input); tiny work_mem elects
sorted-input outright (the hashed arm's spill pricing dominates).

**Evidence** (private SF0.25 clone, `analysis/m0146/m0146-0027/`): Q19
now emits the PG spine — its first divergence moved past the aggregate
stage entirely (was `PG Partial GroupAggregate | goopg Sort`, now a
join-order record at depth 10). Q34/Q42/Q52/Q98 also flipped to the
sorted-input partial but remain masked by their pre-existing
split-vs-nosplit first divergences — no new records. Q62/Q99 still elect
the presorted sibling: the measured contest is si.tot 5966.47 vs sp.tot
5954.83 (~11.6 units of sort-volume delta, inside std-fuzz but past the
tight-fuzz tie-breaker), so the sibling wins on cost. That residual is
epsilon adjudication inside `add_path`'s documented compare — a
cost-margin question for M0146-0007 territory, not a missing mechanism:
the arm exists, prices itself, and wins where the model says it should.

**Correctness**: all five flipped plans execute through
`openSortedPartialEmit` under real Gather Merge — sweep PASS=96 /
0 mismatches / 0 checksum mismatches; Q19 verified 100/100 rows vs the
oracle. Executor identity at 1/2/4 workers
(`TestPartialEmitSortedInputIdentity`) guards the no-over-counting
invariant. Fireset PASS on all changed plans both arms (SF0.25:
Q19/Q34/Q42/Q52/Q98; SF1: Q55); TPC-H acceptance arm 24 MATCH; spotcheck
PASS.

## Slice 4 (2026-09-28): runnable branch pick + PHJ-probe claim wiring

The `PG Gather | goopg Nested Loop Inner` records (Q71, and latently
Q14/Q76) were two compounding defects, not one.

**Producer** — `setOpBranchPick` embedded `branch.PartialPathlist[0]`
unconditionally, matching upstream's `linitial(child->partial_pathlist)`
(allpaths.c:1544) without upstream's invariant: in PG every
partial_pathlist entry is runnable, while goopg's list can carry
executor-refused shapes — Q71's legs lead with a `ParallelHash` partial
that `setOpBranchDrivingKindIsSupported` declines (per-branch PHJ build
state is the ledgered M0140-0006c-3 residual). The produced PathSetOp
could never pass gather admission — a dead-weight partial, so no
`Gather(Parallel Append)` was ever filed. `cheapestRunnableSetOpBranch-
Partial` now picks the cheapest partial the driving-kind predicate
actually admits (`internal/optimizer/windowsetoppaths.go`).

**Executor** — electing the new shape exposed a claim gap in
`attachAll`'s `*setOp` arm (`internal/executor/parallel_scan.go`): the
arm returns after wiring `setOpLeft`/`setOpRight`, so when
`unwrapToSetOp` found the setOp *through a Parallel Hash join's probe
side*, the join's own partial build was never attached to its
`hashBuildBranch` claim set — every participant scanned the whole build
relation into the shared table. First observed as Q71 passing rows=290
with checksum `c59974eb81acf046` (oracle `e9f1fcd7c28a1f8f`); the
isolated repro `(web ∪ catalog ∪ store) ⋈ item` returned 2403 = 3×801.
The arm now also runs `attachParallelHashBuildSides(op)`, which walks
the same probe path and stops at the setOp boundary.

**Result**: Q71 emits `Gather -> NL -> PHJ(Parallel Append, item) ->
Index Scan time_dim` with the exact oracle checksum; Q14/Q76 collapse
`Append -> per-leg Gathers` into `Gather -> Parallel Append -> per-leg
HJs`; Q55 gains `Finalize -> Gather Merge -> Partial GroupAggregate`.
SF0.25 census: parallelism 55 -> 54, D3-partialpath 28 -> 26, records
moved deeper on Q37/Q55/Q71; sweep PASS=96 / 0 mismatches; fireset PASS
(fires Q14/Q71/Q76 both arms; SF1 parallelism 60 -> 58). Tests:
`TestAddPartialSetOpPathPicksCheapestRunnableBranchPartial`,
`TestSetOpBranchPickSkipsUnrunnablePartial`,
`TestAttachAllWiresHashBuildAboveProbeSetOp`,
`TestGatherOverParallelHashProbeSetOpIdentity` (pre-fix: 80/120 rows for
a 40-row join).

## Open

- **Placement residue**: goopg elects a 6-rel partial chain where PG
  stops at 5 (Q17/Q25/Q29's depth-4 `PG NL Inner | goopg Gather Merge`)
  — join-order/costing inside the search, not this mechanism.
- **Q62/Q99 epsilon adjudication**: the sorted-input partial arm landed
  in §8 and cleared Q19's record, but Q62/Q99 still elect the presorted
  sibling — measured ~11.6 cost units of sort-volume delta, inside
  std-fuzz but past the tight-fuzz tie-breaker. The mechanism exists and
  wins where the model prices it ahead; matching PG's election there is
  a cost-margin question (M0146-0007 territory), not reach.
- **Q6**: routed — goopg decorrelates the correlated scalar `avg()` into
  a hash join while PG keeps a `SubPlan` and probes `item` above the
  `Gather Merge`. Owning task M0145-0008y, blocked on M0146-0012
  (correlated restrictions as base-rel index quals). No M0146-0027
  mechanism gap.
- **Q71 residual**: `join-order` at depth 4 under the shared Gather —
  PG index-probes `item` inside the NL chain and runs per-leg Parallel
  Hash joins inside the Append (needs the ledgered per-branch PHJ build
  state); goopg elects a single PHJ over the append instead.
- `presorted > 0` incremental-sort arm: deferred to M0146-0006 as filed.
