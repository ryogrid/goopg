# M0146-0005dj: Hash Right Semi / Right Anti joins (PG 18)

Status: slice 1 landed 2026-10-03 (executor substrate). Still to come:
- slice 2: the planner producer and costing;
- slice 3: the parallel right join.

## What PG does

For a semi or anti join whose preserved side is cheap to hash, PG 18 also
considers the join with its sides swapped. In joinrels.c, `make_join_rel`
calls `add_paths_to_joinrel(rel2, rel1, JOIN_RIGHT_SEMI)` next to the
JOIN_SEMI call, and the JOIN_RIGHT_ANTI call sits next to JOIN_ANTI. These
paths are hash joins only:

- the semi side becomes the inner, hashed input;
- the other side probes it;
- `HeapTupleHeaderSetMatch` marks each build tuple a probe tuple matches.

JOIN_RIGHT_SEMI emits a build tuple on its first match and skips it once it
is marked. JOIN_RIGHT_ANTI emits nothing while probing; the
HJ_FILL_INNER_TUPLES sweep then emits the unmarked build tuples
(nodeHashjoin.c). TPC-DS Q23 (Hash Right Semi Join), Q69 (Hash Right Anti
Join) and Q75 (Parallel Hash Right Join) print these shapes in PG. goopg had
none of them.

## Slice 1: representation and executor

- `optimizer.JoinTypeRightSemi` / `JoinTypeRightAnti` are appended to the
  JoinType enum, with `IsRightSemiAnti`. A Join of these types follows
  goopg's lowering convention: Left = outer = probe, Right = inner = build.
  Its `Output()` is the RIGHT input's schema, the preserved side.
- The executor (`operators_join_agg.go`, `join_outer_fill.go`):
  - hash algorithm only; any other algorithm is an internal error;
  - never builds on the left;
  - `buildMatchTracked` keeps the per-bucket matched bitmap for both types.
    The bitmap is set after the residual predicate, so a key hit the
    residual rejects is not a match.
  - The drain loop skips an already-marked build row, marks a new one, and
    for RIGHT SEMI emits it alone (`buildOnlyEmit`).
  - RIGHT ANTI is a fill-build join: the existing per-batch sweep and the
    NULL-key sweep emit the unmarked build rows, also build-only.
- Hybrid-hash batching is admitted for both: the bitmap and the sweep are
  per batch, like RIGHT/FULL. The cooperative shared build declines them
  (`parallel_hash_build.go`), like RIGHT/FULL.
- EXPLAIN labels them "Hash Right Semi Join" / "Hash Right Anti Join".

No producer emits these types yet, so no plan changes. The fire set shows
none.

## Verification

- `TestHashRightSemiAntiJoinEmitsBuildRows` compares both types against a
  Go reference, in memory and under a 256 KiB work_mem that forces a
  multi-batch build. It includes duplicate keys on both sides and
  NULL-keyed rows on both sides.
- `TestHashRightSemiAntiHonourResidual` checks that a residual-rejected
  key hit neither emits (semi) nor suppresses (anti).
- `TestRightSemiAntiJoinLabels` checks the EXPLAIN labels.
- Gates pass: units, spotcheck, sweep 96/96, arm, fire set (no plan
  changed), ea-ratchet.
- The first sweep run hit ERROR=10. Those were memory-guard kills of the
  sweep's server under host-wide pressure while the nightly batch ran; the
  re-run passed.

## Slice 2 — the planner producer, and PG's semi/anti hash cost

### Producer

`make_join_rel` adds, for a JOIN_SEMI special join, the commuted
`add_paths_to_joinrel(rel2, rel1, JOIN_RIGHT_SEMI)`; the same holds for
JOIN_ANTI and JOIN_RIGHT_ANTI. goopg runs the commuted direction through
`addPathsToJoinrel`, so the hook sits there, before
`jointypeForDirection`'s early return for that direction:

- `rightSemiAntiForDirection` fires only when the outer covers the special
  join's min_righthand and the inner covers its min_lefthand. A null-aware
  anti (a NOT IN over a nullable operand, `SpecialJoinInfo.NullAware`,
  set by `inUnnestSJInfo`) is excluded, as PG excludes JOIN_ANTI with
  null-aware semantics.
- `addRightSemiAntiHashPath` builds the serial hash path only. RIGHT SEMI
  is hash-only in PG. Merge right anti and the parallel forms are not
  built (see Next). Cost: `final_cost_hashjoin`'s generic bucket walk,
  which is what PG uses for both right types.
- Lowering: `planJoinTypeFor` maps the parser types to the plan types.
  `publishedSchema` / `publishedLayout` publish the inner (build) side
  only, and `narrowcostinputs` takes the output width from the inner.
- New parser JoinType values `JoinRightSemi` / `JoinRightAnti`, planner
  internal only.

### The semi/anti hash join was priced by the generic arm

`final_cost_hashjoin` sends JOIN_SEMI, JOIN_ANTI and inner-unique joins
through its early-exit branch. A matched probe stops at its first match,
so it walks `inner_scan_frac = 2/(match_count+1)` of its bucket. An
unmatched probe walks the average virtual bucket at one tenth of the
cost. hashjointuples is the matched rows (the unmatched rows for ANTI).
goopg routed only inner-unique joins there. Semi and anti took the
generic walk, which priced every probe at half a full bucket.

Once the right forms competed, this flipped plans away from PG. On the
`subselect` regress table (1850 default rows per side), goopg's two-key
Hash Semi cost 958.88 against PG's 312.64. A Right Semi at 950.49, which
equals PG's own cost for it, therefore won, and a no-ORDER-BY result came
out in a different order.

`hashJoinFinalCostInputFor` now gives SEMI / ANTI the pair's
`semiAntiJoinFactorsFor`, the factors the nested-loop arm already reads.
`hashJoinCost` takes the early-exit branch for them and charges
cpu_tuple_cost on the unmatched rows for ANTI. Probes on scratch PG 18.3
and goopg match to the cent:

| shape | PG | goopg before | goopg after |
|---|---|---|---|
| two-key semi | 312.64 | 958.88 | 312.64 |
| one-key semi | 116.08 | 521.81 | 116.08 |
| two-key anti (merge off) | 321.90 | — | 321.90 |
| filtered one-key anti | 102.98 | — | 102.98 |

## Slice 2 verification

- `TestSearchElectsRightSemiAntiJoins` (executor) plans an EXISTS and a
  filtered NOT EXISTS through the search with ANALYZE stats. It asserts
  that a Right Semi / Right Anti join is elected, as PG 18.3 does on the
  same data, and that the rows are right.
- `TestHashJoinFinalCostInputPreservesNonInnerJoinTypes` now pins the
  early-exit inputs and the matched-walk and ANTI tuple terms.
- Regress A/B against HEAD. `join` shrinks 18570→18554 lines: four of
  PG's Hash Semi / Anti Joins now match where goopg used a Nested Loop,
  and PG's `tbl_rs` Hash Right Semi Join now matches. `subselect` shrinks
  2784→2781. `with`, `aggregates`, `select_distinct`, `union`,
  `join_hash`, `partition_join` and `select_parallel` are byte-identical.
  No result row changed. (`rowsecurity`'s `\dp` lists the tables that
  earlier tests in the same run left behind, so its size depends on the
  set run.)
- Gates pass: units, spotcheck, sweep 96/96, TPC-H arm (24 MATCH),
  ea-ratchet (10/10), fire set (Q10 Q33 Q35 Q56 Q58 Q60 Q69 Q83 fire, all
  value-PASS).
- The only captured plan that changed is SF0.25 Q83. Its catalog_returns
  subtree now has PG's Hash Semi Join over Nested Loop + Memoize
  (`date_dim_pkey`) with serial `date_dim_7`/`date_dim_8` scans; the other
  fires moved cost only. match / text-identity are flat (SF0.25 39 / 35,
  SF1 28 / 20). The classifier newly tags Q83 `scan-type` (28→29), even
  though three more of its scan nodes now agree with PG; the tag is
  positional.
- goopg elects no right form in TPC-DS yet. PG's Q23 / Q69 right joins sit
  over subtrees that goopg shapes differently: Q69's NOT EXISTS inputs are
  estimated at 75 rows against PG's 818, goopg uses Parallel Hash Anti
  Joins, and it lacks the unique-ified semi inners.

## Slice 3 — Parallel Hash Right / Full / Right Anti

### Why only a Parallel Hash

A RIGHT, FULL or RIGHT ANTI join emits the build rows no probe row
matched. Under a Gather the probe input is partitioned, so no single
participant sees every match. PG's `hash_inner_and_outer` therefore
files these jointypes only with a partial inner, as a shared Parallel
Hash table, and sets `cheapest_safe_inner = NULL` for them: "no one
process has all the match bits". JOIN_RIGHT_SEMI is excluded from the
parallel block entirely, because its emit-once decision is taken at the
first match, in whichever participant finds it.

goopg follows the same split:

- `hashJoinIsPartialCapable` admits JoinTypeRight / JoinTypeFull /
  JoinTypeRightAnti only when `Join.ParallelHash` is set.
- `partialHashJoinTypeOK` files the parser types, and
  `partialHashJoinNeedsSharedTable` makes `addPartialHashJoinPath` stop
  after the Parallel Hash variant for them.
- `addRightSemiAntiHashPath` files the partial RIGHT_ANTI. RIGHT_SEMI
  stays serial.
- `TestPartialHashJoinTypeOK` pins the producer against the executor for
  every jointype, both with and without a shared table.

### The executor's match-bit reduction

The Parallel Hash table (M0146-0002) is one table shared by pointer.
Every participant names a build row by the same bucket key and row
position, so a participant's private matched bitmap is already
meaningful for everyone else:

- After the build barrier, a fill-build join attaches its participant to
  the probe phase (`parallelHashBuild.probeAttach`).
- At probe EOF, `probeDetach` ORs the participant's bitmaps into the
  merged set under the mutex. The participant whose detach brings the
  prober count to zero claims the sweep and receives the merged bits;
  the others end without sweeping. This is PG's "last participant to
  detach from the probe phase scans for unmatched tuples"
  (ExecParallelPrepHashTableForUnmatched).
- A participant arriving after the claim probes nothing. A participant
  reaches probe EOF only after the shared partial scan is exhausted, so
  nothing is left for it to probe.
- NULL-keyed build rows never match. Each participant emits the ones it
  built itself.
- The probe loop is unchanged. Marks stay in the private `[]bool`
  bitmaps, so a Parallel Hash join adds no atomic operation per match.
  The mutex at detach is the publication edge.
- A Parallel Hash build never batches: a spilling participant fails with
  `errParallelHashSpilled`, and the planner's PH4 veto keeps such inners
  off the path. Per-batch sweeps therefore never arise.

### Slice 3 verification

- `TestParallelHashFillBuildIdentityWithSerial` runs RIGHT, FULL and the
  planner-elected RIGHT ANTI as a 4-worker Parallel Hash and checks
  parallel-vs-serial row identity (58782, 63951 and 4024 rows; 5
  builders each). It also checks that the sweep was claimed exactly once.
  Mutation check: with every participant sweeping on its own bits, all
  three cases fail with duplicated unmatched rows (64200 / 69587 / 27780
  rows). The real code is clean under `-race`.
- `TestParallelHashProbeDetachMerges` pins the protocol: bits merge,
  there is one claim, and a late attach is refused.
- TPC-DS fire set: Q69 / Q75 fire at SF0.25, Q5 / Q75 at SF1, and all
  values pass.
  - SF1 Q75's three Parallel Hash Right Joins now match PG line for
    line, including `Workers Planned: 2` (was 4).
  - SF1 Q5 gains PG's Parallel Hash Right Join.
  - SF0.25 Q69 now uses a Parallel Hash Right Anti Join, where PG runs
    serial Hash Right Anti Joins above its Gathers.
  - Aligned PG lines: SF0.25 2319→2332, SF1 2112→2148. match /
    text-identity are flat (39/35, 28/20).
- Regress A/B: `join_hash`, `select_parallel`, `partition_join` and
  `subselect` are byte-identical. `join` moves by one line: a
  no-ORDER-BY row order that also toggled in slice 2's A/B.
- `join_hash`'s two Parallel Hash Full Join plans stay unmatched: goopg
  plans FULL joins outside the join search (`jointypeForDirection`
  reports FULL as not legal) as Merge Full Join. The executor now runs a
  parallel FULL hash join, but no path reaches it (ledgered).
- Gates: units, spotcheck, sweep 96/96, TPC-H arm, ea-ratchet, fire set.

## Residuals (ledgered)

- Merge right anti (PG builds one; goopg's merge executor has no
  RIGHT ANTI arm).
- `innerrel_is_unique` is not run on the commuted pair, so a right join
  over a unique build side is priced by the non-unique arm.
- FULL joins never reach the search, so neither serial nor Parallel Hash
  FULL is elected (join\_hash's two plans).
- Q23's Hash Right Semi and Q69's serial right anti sit under subtrees
  goopg shapes differently (row estimates, unique-ified inners —
  M0146-0005dk).
