# M0146-0144 — no plain nested loop for JOIN_RIGHT

Status: done 2026-10-10 (0ffe3f88b). Parent: M0146-0141.

## Problem

After M0146-0141, TPC-DS Q72 at SF1 joined `promotion` as a
`Nested Loop Right Join`:

- `promotion` (300 rows) was the outer;
- a `Materialize` of the 2-row join was the inner.

PG can never build that plan. PG's plan is a `Nested Loop Left Join` over
the 2-row join, with a Materialized `promotion` inner.

## PG behaviour

`match_unsorted_outer` (joinpath.c) gates the nested-loop arms on
`nestjoinOK`: "Nestloop only supports inner, left, semi, and anti joins".

- JOIN_RIGHT, JOIN_RIGHT_ANTI and JOIN_FULL set `nestjoinOK = false`.
- JOIN_RIGHT_SEMI returns before any arm.
- The parallel arm has the same set (`consider_parallel_nestloop` is
  never called for them).

So a LEFT join's nested loop always has the preserved side outer. The
RIGHT orientation of the same pair carries only hash and merge paths.

## Change

- **`nestJoinOK`** (joinpaths.go) returns false for JoinRight,
  JoinRightAnti, JoinRightSemi and JoinFull.
- **Where it gates.** `addPathsToJoinrel` checks it before the plain
  nested loop (`addNestLoopPath`) and the materialised one
  (`addMaterialNestLoopPath`).
- **Arms that already refused RIGHT.** The parameterised arm
  (`addNLIPaths`, R64) and the partial arm (`partialHashJoinTypeOK`) need
  no change.
- **Why removing the paths is safe.** The commuted call still offers its
  hash and merge family, and the forward (LEFT) call still offers the
  nested loop. A LEFT/RIGHT pair therefore never loses its last path, just
  as in PG.

## Verification

- **Test.** `TestAddPaths_NoPlainNestLoopForRight`, asserted on the DPPATH
  trace (that is, on what is generated, not on what survives):
  - the LEFT direction offers `join.nestloop`;
  - the RIGHT direction offers none;
  - the four refused join types and the four admitted ones are checked.
  It fails with the gate disabled (2 RIGHT nested loops offered).
- **TPC-DS fire set.** Only Q72 at SF1 fires; SF0.25 has none.
  - Q72's top join is now PG's `Nested Loop Left Join` (Join Filter
    `cs_promo_sk = p_promo_sk`) over `Materialize -> Seq Scan on
    promotion`. Total cost is 61205.87, against 61205.12 for the
    PG-impossible plan.
  - SF1 `qual-placement` reads 9 → 10. That is a diff-alignment artifact,
    not a new divergence:
    - goopg still sorts above the join, where PG sorts per worker under a
      Gather Merge (`sort-strategy`);
    - so the verbose diff pairs goopg's nodes one level off PG's;
    - its new qual-placement line compares goopg's
      `Join Filter (cs_item_sk = i_item_sk)` nested loop against a
      different PG node.
  - Matches are unchanged (SF0.25 53, SF1 39).
- **TPC-H.** Plans are byte-identical; the acceptance arm matches on
  values.
- **Regress A/B** (32 cases, against the M0146-0143 build, which is HEAD's
  code). No case regressed.
  - `rangefuncs` loses 116 diff lines: LEFT JOINs over a function scan now
    return rows in PG's order.
  - `join` loses 53. One PG-impossible `Nested Loop Right Join` becomes a
    `Hash Right Join`; both still differ from PG's VALUES-outer nested
    loop.
- **Gates.** Units, TPC-H spotcheck, SF0.25 sweep (PASS=99) and
  ea-ratchet (1) all PASS. The first acceptance-arm and sweep attempts
  refused to run while the nightly CI batch was active; both were re-run
  after it ended.

## Not covered

- **Q72's remaining divergences** are the same as M0146-0141's:
  - the `d3` probe is a Bitmap Heap Scan where PG uses an Index Scan;
  - PG sorts per worker under a Gather Merge for the GroupAggregate.
  Both are already ledgered (M0146-0141 row).
