# M0146-0027 — parallel partial-subtree reach (slice 1)

Filed 2026-09-27 by M0146-0005y's SF0.25 residual re-routing: the last
unowned first-divergence family (~10 records, Q6/Q17/Q25/Q29/Q50/Q77 and
kin). PG's `generate_useful_gather_paths` covers the whole parameterised-NL
chain and a `Gather Merge` over per-worker Sort carries ordering through
the NLs above it; goopg's Gather landed deeper (in Q17 on `sr ⋈ d2`) and
the Sorts above the join spine were unreachable to `findPartialSubtree`
(the `subtreeHasGather` stand-down at `parallel.go:191`) — which is also
why `GOOPG_PARTIAL_SORT_PATHS=on` moved zero plans on this corpus.

## What landed (slice 1)

All in `internal/optimizer/`:

1. **`gatherpaths.go` — the sorted-partial arm** (allpaths.c:3255-3341's
   missing second half). `generateUsefulGatherPaths` now walks
   `usefulPathkeysForRelation` (= `get_useful_pathkeys_for_relation`'s one
   populated arm, gated by `pathkeySortableEarly` =
   `relation_can_be_sorted_early`: key computable from the reltarget —
   `itemSpans`-resolved rels ⊆ `rel.Relids` — and parallel-safe via
   `isParallelSafeExpr`). For each partial path not already delivering a
   useful ordering it prices `Gather Merge -> Sort -> <partial>` via
   `sortPathForBounded` (per-worker rows convention matches
   `create_sort_path`'s worker-side pricing) and files it as
   `gather.merge.sort`. Already-sorted partials keep the bare
   `Gather Merge`; the `presorted > 0` incremental-sort arm stays deferred
   to M0146-0006 (upstream would not price a plain Sort there).
   - `sortPathForBounded` now propagates `sub.ParallelWorkers` —
     upstream `create_sort_path` copies `subpath->parallel_workers`.
   - `searchCtx.itemSpans` (stamped in `searchOneProblem`) carries the
     joinlist item-coordinate windows `pathkeySortableEarly` needs.
   - `partialPathDrivingKind` admits `PathSort` (executor `sortOp` arm
     already supports it under all partial descents).

2. **`upperordered.go` — `addOrderedPaths`' `is_sorted` arm**
   (planner.c:5342-5345). `SearchCandidates`/`SearchCandidateKeys` (the
   M0141-S2b plumbing that previously had no reader) are now consumed:
   every searched candidate whose re-validated pathkeys contain the
   ORDER BY ordering is rebuilt through `searchedCandidateInput` and
   offered — the "ordered runner-up loses to cheapest-total" gap.

3. **`groupingpaths.go` — `upper.groupagg.searchcand`**
   (planner.c:7134's `input_rel->pathlist` iteration). Same mechanism at
   the GROUP BY stage: candidates whose keys contain the group ordering
   are rebuilt and wrapped in a sorted `PathAgg`; `addPath`/`setCheapest`
   adjudicate normally — no forcing.

4. **`searchedtree.go` — `searchedCandidateInput`** —
   `searchedCheapestTotalInput` parameterized on the path: rebuilds a
   candidate through the SAME boundary the winner used (`BoundaryFill`
   replays the hole-filler license the committed publication ran under —
   the `hole at 24` decline), re-wraps pass-through Project/Sort nodes,
   verifies output-schema equality, recovers internal panics into a
   decline.

5. **`upperorderedinput.go` — `aggregateEmissionPathkeys`** gained a
   searched-root arm: a sorted aggregate whose child is a searched-tree
   root carrying validated `searchedPathkeys` claims the ordering, so the
   ORDER BY stage does not stack a redundant top Sort.

## Measurement (SF0.25, 2026-09-27, plans-20260927-183227 vs baseline
plans-20260927-163116)

- Census (`census-sf025-slice1.txt`): divergent 89 → **88**, matches
  10 → **11**, sort-strategy records 43 → 38.
- **Q7 → MATCH** (was `PG Gather Merge | goopg Sort` under GroupAggregate).
- Q17/Q25/Q29: first divergence pushed from `depth=2 [sort-strategy] Sort
  under GroupAggregate` to `depth=4 [parallelism]` — goopg now emits
  `Gather Merge -> Sort -> partial NL` and the residual is WHICH joinrel
  the Gather Merge covers (goopg's 6-rel chain vs PG's 5-rel — cs lands
  inside vs above), a join-order/costing residue.
- Q50: pushed to `depth=6 [qual-placement]` — both sides Nested Loop now.
- Q77: `depth=7 [scan-type]` (Index Only vs Index Scan on store_pkey) —
  the sort-strategy record is gone.
- Q26/Q59/Q84: changed category (Q59 moved toward PG's serial
  HashAggregate; see slice README note below).
- Q6 unchanged: its divergent arm is a different composition (Sort under
  GroupAggregate with no qualifying partial chain — PG reaches its shape
  through a partial the search never files).

### Q17 witness (full plans in `q17-*.txt`)

Before: `GroupAggregate -> Sort -> NL -> ... -> Gather(deep, sr⋈d2⋈cs)`.
After: `GroupAggregate -> NL -> NL -> Gather Merge -> Sort -> NL partial
chain` — PG's spine (PG: `GroupAggregate -> NL -> NL -> NL -> Gather
Merge -> Sort -> partial chain`). Result: 0 rows both.

## Gates (all green; stamp FAIL marker is the dirty-tree note only)

- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — pass.
- `scripts/tpch-spotcheck.sh` — Q12 rows=2, Q13 rows=33, RESULT=PASS.
- `scripts/tpcds-sf025-regression.sh sweep` — PASS=96 MISMATCH=0
  CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3; plans: same=70 changed=29.
- Runtime: total +0.6%. Q59 1s→5s investigated: moved to PG's own serial
  `HashAggregate -> Hash Join` shape (parity gain, cost residual).
- New test `TestGenerateUsefulGatherPathsSortedPartialArm`; updated
  upperorderedinput (parallel-off negative) + joinpathspartialmerge
  (PathSort descent) tests.

## Residual / next steps

- **Gather Merge placement** (Q17/Q25/Q29 depth-4 records): which joinrel
  the partial chain covers is a costing decision inside the search —
  goopg elects the wider partial; PG stops one level earlier.
- **`Unique`/`Group`/`Limit`-head records**: the same
  all-pathlist iteration exists only at the ORDER BY and GROUP BY
  stages; `PG Gather Merge | goopg Sort` records under Unique/Group heads
  need the consumer ported there (small, same seam).
- **Q6**: different composition — no qualifying partial exists on its
  spine to sort; likely needs the partial-join election widened, not this
  mechanism.
