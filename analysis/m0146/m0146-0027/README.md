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

---

# Slice 2 (2026-09-27): partial DISTINCT — `Unique -> Gather Merge -> Unique`

The "Unique-head needs the `is_sorted` consumer" hypothesis above was
wrong in a useful way: tracing Q38/Q87 showed PostgreSQL never routes
DISTINCT through pathlist iteration at all — it files a dedicated
`UPPERREL_PARTIAL_DISTINCT` stage (`create_partial_distinct_paths`,
planner.c:4852): per-worker `Unique` over a per-worker `Sort`, gathered
ordered, re-deduped by a leader `Unique`. goopg had no counterpart —
`createDistinctPaths` offered only serial-seed candidates — so the shape
was unreachable, not merely unpriced.

## What landed (slice 2)

- `distinctpaths.go` — `addPartialDistinctPaths`: the producer, on the
  M0146-0025 partial-Group node-level model. Sort-strips the serial
  candidate's input, unwraps/splices a search-placed `Gather` (Q38's
  input is `Sort{Gather{NL}}`), per-worker `Sort` + marked worker
  `Unique` (`PartialUnique`), `Gather Merge` crossing
  `partialGroups × d` rows, unmarked final `Unique`. Sorted arm only —
  upstream's partial hashed DISTINCT and LIMIT-1 arms declined, never
  approximated.
- `plan.go` — `PartialUnique` on `Distinct` (spec) + `DistinctOn`
  (node); `createDistinctPlan` propagates it. The marker is the walk
  contract: only a marked node is transparent to the parallel descents.
- `parallel.go` — gated `*DistinctOn` arms in `stampParallelScan`,
  `drivingScan`, `drivingScanCrossesSort`, `unstampParallelScan`.
- `executor/parallel_scan.go` — the same gate in `attachParallelScan`,
  `attachParallelIndexScan`, `attachParallelBitmapScan`. An unmarked
  dedup under a Gather refuses attachment rather than emit each
  cross-partition duplicate once per worker.

## Evidence

- `q38-goopg-plan.txt` / `q87-goopg-plan.txt` — all three `Unique` heads
  under Q38's `HashSetOp Intersect` emit
  `Unique -> Gather Merge -> Unique -> Sort -> <partial>`, PG's spine.
- Sweep `sweep-20260927-201641.txt`: PASS=96 MISMATCH=0 CKMISMATCH=0;
  Q38 ck=`77188220d949e451`, Q87 ck=`daa38faef432c025` — oracle-equal.
  Plan channel: changed = Q38/Q54/Q87 (exactly the clause-level
  DISTINCTs); Q54 PASS with the same architecture.
- Executor identity `TestPartialUniqueGatherMergeIdentity` (1/2/4
  workers): partial dedup + leader dedup = serial `SELECT DISTINCT`
  row set — cross-worker duplicates collapse exactly once.

## Gates (slice 2; stamp FAIL markers are the dirty-tree note only)

- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — pass.
- `scripts/tpch-spotcheck.sh` — Q12 rows=2, Q13 rows=33, RESULT=PASS.
- `scripts/tpch-acceptance-arm.sh on` vs `tmp/m0145-0008m/arm-on.txt` —
  SUMMARY 24 MATCH, VERDICT PASS.
- `scripts/tpcds-sf025-regression.sh sweep` — PASS=96 MISMATCH=0
  CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3; plans: same=96 changed=3
  (Q38/Q54/Q87). Total runtime −4.0%; Q95 2s→6s noise (`distinct` there
  is `count(distinct)` — aggregate, not this node kind; plan unchanged).
- `scripts/tpcds-fireset-gate.sh m0146-0027s2` — fires Q38/Q54/Q87 PASS
  on both arms at both scales, values identical, no new timeouts.
  Census (slice2 vs baseline captures): divergent 88→88, sort-strategy
  38→36 — Q38/Q87's Unique-head records are gone; they now diverge at
  `SetOp Intersect/Except` vs `HashSetOp …` kind (depth 4→2/1, a
  different mechanism family). Full record in `gates-slice2.txt`.

## Residual / next steps (post slice 2)

- The `is_sorted` consumer for `Group` heads and the
  `PG Partial GroupAggregate | goopg Sort` records (Q19/Q62/Q99): need a
  sorted per-worker GroupAggregate transport — a different mechanism
  (M0146-0025's emit transport is hashed).
- Q12/Q20/Q73's `PG Gather Merge | goopg Sort` records: measured 0.08
  cost-tie losses (the searchcand arm files the right shape; election
  margin, M0146-0007 territory), not reach.
- Placement residue (Q17/Q25/Q29 depth-4) and Q6 unchanged from slice 1.
- Upstream's partial hashed-DISTINCT arm and empty-pathkeys LIMIT-1 arm:
  declined by construction (ledger-recorded).
