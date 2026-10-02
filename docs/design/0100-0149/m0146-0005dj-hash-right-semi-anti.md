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

## Next

- **Slice 3.** Parallel Hash Right / Right Semi / Right Anti (TPC-DS SF1
  Q16's Parallel Hash Right Anti Join), which need matched flags shared
  across workers. Merge right anti belongs here too.
- Inner-unique proof for the right types (`innerrel_is_unique` on the
  commuted pair) is not run, so a right join over a unique build side is
  priced by the non-unique arm.
