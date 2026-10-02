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

## Next

- **Slice 2 (planner).** Generate the swapped semi/anti hash path in the
  search; `make_join_rel`'s JOIN_SEMI / JOIN_ANTI arms are the model.
  Cost it as `final_cost_hashjoin` does for JOIN_RIGHT_SEMI / RIGHT_ANTI.
  Lower it with the output layout taken from the build side. Audit every
  optimizer JoinType switch (about 98 Semi/Anti sites).
- **Slice 3.** Parallel Hash Right / Right Semi / Right Anti, which need
  matched flags shared across workers.
