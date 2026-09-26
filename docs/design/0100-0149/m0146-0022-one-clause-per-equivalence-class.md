# M0146-0022: an inner join applies one clause per equivalence class

Status: landed 2026-09-26 (placement; sizing and special joins open).

## PG behaviour

PG does not apply the equalities as the user wrote them. `process_equivalence`
(`postgres/src/backend/optimizer/path/equivclass.c`) folds every mergejoinable
`a = b` into an EquivalenceClass. `build_joinrel_restrictlist` then asks
`generate_join_implied_equalities` for each class that spans the join, and
`generate_join_implied_equalities_normal` emits exactly one clause per
(outer, inner) split. Its comment gives the reason: "we can equate any one
outer member to any one inner member", because each side's members are
already equal inside it.

- The clause is the best-scoring outer member × inner member pair. Every
  member goopg forms a class from is a plain Var of one type. All pairs
  therefore tie at the top score, and the first outer member (in
  `ec_members` order) × the first inner member wins.
- `ec_members` order is the order `process_equivalence` added the members
  while walking the quals:
  - a new pair opens a class `[l, r]`;
  - a new operand appends to its partner's class;
  - a merge appends the right operand's class after the left's.
- The clause is written outer = inner (`create_join_clause(…, outer_em,
  inner_em, …)`). PG reuses a source clause with that exact orientation and
  otherwise builds one.

TPC-DS Q74's four-way self join of `year_total` shows the difference. PG's
Join Filter carries one equality, `t_s_secyear.customer_id =
t_w_firstyear.customer_id`, which was never written. goopg's carried every
written and inferred equality between the two sides: three or four
conjuncts per join.

## Change

- `buildRestrictInfos` replays `process_equivalence` over the column
  equijoins in list order and records each class's member order
  (`restrictInfoList.ecMembers`) and every equated member pair (`ecPairs`).
- `reduceEquivClassJoinClauses` is the inner-join arm of
  `buildJoinRelRestrictList`. A class with two or more applicable clauses
  keeps one: the first outer member = the first inner member. That clause
  is reused when it is written that way; otherwise it is flipped, and the
  copy is cached so it keeps one identity across pairs (PG's `ec_derives`).
- It is fail-closed. The class keeps every clause unless all of these hold:
  - each member on each side comes from a distinct input rel;
  - every pair of same-side members is equated by some clause in the list,
    so a lower join already applied it (the seam's transitive closure,
    `inferTransitiveEqualities`, supplies these pairs);
  - a clause for the chosen pair exists.
- `ecReduce` is set only for a join problem with no special joins. An
  outer join's nullable member is not equal to its class-mates once
  null-extended, so the "sides already equal" argument fails there.
- Join sizing is unchanged. `sizeJoinRel` reads
  `joinRelSizingClauses` (the unreduced list), so estimates still come
  from `oneClausePerEquivClass`' explicit-first member.

## Results

- SF0.25 census: Q31 and Q74 → MATCH, and Q24 moves from depth 4 to 6.
  Qual-placement records 26 → 25 (CATEGORIES-EXCL-MATCH).
- SF1 census: Q24 moves from depth 4 to 7 (next record: Parallel Hash
  Join). Qual-placement records 23 → 20.
- TPC-H census: Q9 → MATCH (22 queries: 6 match).
- Rows unchanged: sweep 96/96, fire set 17 fires with none timing out,
  spotcheck, acceptance arm 24 MATCH, regress 41 = HEAD.
- Evidence: `analysis/m0146/m0146-0022/`.

## Open (ledgered)

1. Sizing still charges the explicit-first member. PG estimates the join
   with the generated clause (`clauselist_selectivity` over the reduced
   restrictlist).
2. Problems with special joins keep every clause. PG's classes stop at
   outer joins through `ec_below_outer_join` / `reconsider_outer_join_clauses`.
   Porting that bookkeeping would let the inner joins of such problems
   reduce too.
3. A class with a constant (`ec_has_const`) generates no join clause in PG:
   every member gets `var = const` instead. goopg still applies one.
