# M0146-0129 — a constant-FALSE outer-join qual makes the nullable side a dummy rel

Status: done 2026-10-09 (f54d6219a). Parent: M0146-0123.

## Problem

The M0146-0123 ledger recorded that `t LEFT JOIN u ON false` planned the
whole nullable side, printed as `Join Filter: (false)` over `Seq Scan on u`.
PG instead prints `Join Filter: false` over a childless
`Result  One-Time Filter: false`.

Against PG's expected output, regress `join.sql` was missing 19 such
`One-Time Filter: false` lines.

In `subselect.sql`, `left join (select json_array(1, a) ...) on false`
even failed: goopg evaluated the nullable side it never needed.

## PG behaviour

`populate_joinrel_with_paths` (joinrels.c), JOIN_LEFT arm: when
`restriction_is_constant_false(restrictlist, joinrel, false)` holds — any
clause in the join's restriction list is a constant FALSE or NULL — and
`rel2` lies within the special join's RHS, `mark_dummy_rel(rel2)` is
applied.

The dummy rel keeps its reltarget. A PlaceHolderVar above the join
therefore still deparses as its expression, as in `((1) IS NULL)`.
JOIN_FULL has no such arm.

## Change

- **The pass.** `dummyConstantFalseOuterJoinSides` (outerjoin_dummy.go)
  runs per scope right after `gatePseudoconstantQuals`. It walks the tree
  in place (`setPlanChildrenInPlace`) and replaces the nullable input of
  every LEFT or RIGHT `Join` whose predicate has a constant FALSE or NULL
  conjunct with a dummy `Result` of the same schema.
  - FULL joins are untouched.
  - A kept CTE's body is shared between references, so the walk does not
    enter it.
- **The dummy's targets.** `constantOutputExprs` follows column references
  through Project and Result nodes, and a one-row VALUES, to the
  expression that computes each output. A target that reads no column and
  holds no sublink stays that expression; the others become identity
  column references.
- **EXPLAIN rendering.**
  - The key-source chase (`resolveKeySourceAt`) gains a childless-Result
    arm to match the Project arm, so `((1) IS NULL)` prints as in PG.
  - A lone constant Join Filter prints bare (`Join Filter: false`), as
    `show_qual` does over a Const.
  - An explicit cast of NULL prints as the NULL Const it is after parse
    analysis (`NULL::boolean`, `get_const_expr`) instead of
    `(NULL)::boolean`.

## Verification

- **Test.** `TestConstantFalseOuterJoinDummiesNullableSide` covers LEFT and
  RIGHT joins, FULL and inner joins, a non-constant predicate, and a nested
  join.
- **Scratch probe vs PG 18.3.** Every LEFT JOIN `ON false`, `ON a AND
  false` and `ON NULL::boolean` shape matches, and the results match.
- **Regress A/B** (32 cases):
  - `join` goes 14699 → 14679 diff lines;
  - `subselect` goes 1367 → 1354, and the `json_array` query now returns
    PG's rows;
  - `predicate` and `union` only change `Join Filter: (true)` → `true`.
- **TPC.** The TPC-DS fire set changes no plan, and TPC-H plans are
  byte-identical.
- **Gates.** units, TPC-H spotcheck, acceptance arm, fire set, SF0.25
  sweep and ea-ratchet all PASS.

## Not covered (ledgered)

- **RIGHT JOIN orientation.** `u RIGHT JOIN t ON false` stays a
  `Nested Loop Right Join` in goopg, where PG plans a Left Join with `t`
  outer. FULL JOIN `ON false` is a Merge Full Join in PG.
- **Constant-TRUE join quals.** PG removes them, but goopg keeps them as
  `Join Filter: true`.
- **Contradictory constants.** `p.k = 1 AND p.k = 2` in one equivalence
  class is a dummy query in PG, and goopg still plans it.
- **Dummy propagation.** PG's other dummy-rel propagation is not ported:
  an inner join with a dummy input becomes dummy, and so does a LEFT join
  whose outer side is dummy.
