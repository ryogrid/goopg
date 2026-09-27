# M0146-0027: parallel partial-subtree reach — sorted-partial Gather Merge + searched-candidate consumption

Status: landed 2026-09-27 (slice 1 — ORDER BY / GROUP BY stages; the
`Unique`/`Group`-head consumers and the Gather-Merge placement residue are
open, see §6).

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

## Open

- **Placement residue**: goopg elects a 6-rel partial chain where PG
  stops at 5 (Q17/Q25/Q29's depth-4 `PG NL Inner | goopg Gather Merge`)
  — join-order/costing inside the search, not this mechanism.
- **`Unique`/`Group`-head consumers**: the `is_sorted` iteration exists
  only at the ORDER BY and GROUP BY stages; the remaining `PG Gather
  Merge | goopg Sort` records under Unique/Group heads need the same arm
  there.
- **Q6**: different composition — no qualifying partial exists on its
  spine.
- `presorted > 0` incremental-sort arm: deferred to M0146-0006 as filed.
