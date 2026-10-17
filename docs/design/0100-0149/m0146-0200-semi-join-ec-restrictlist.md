# M0146-0200 — a semi join's restrict list takes the class's first-member clause; anti quals stay out of the classes

Status: done 2026-10-16 — TPC-H Q21 matches PG.

## Problem

TPC-H Q21 joins `orders`, `lineitem l1`, the EXISTS RHS `l2` and the NOT
EXISTS RHS `l3`. PG's `Nested Loop Semi Join` over the parameterised `l2`
probe (`Index Cond: l_orderkey = l1.l_orderkey`) carries `Join Filter:
(orders.o_orderkey = l2.l_orderkey)`. goopg printed the same plan without the
Join Filter.

In PG the semijoin qual `l2.l_orderkey = l1.l_orderkey` is an ordinary
equivalence-class member: `distribute_qual_to_rels` sees no nullable side,
so `maybe_equivalence` holds. The class is {o_orderkey, l1.l_orderkey,
l2.l_orderkey}.

`build_joinrel_restrictlist` takes
`generate_join_implied_equalities_normal`'s one clause for the class: the
first outer member in `ec_members` order against the inner member,
`o_orderkey = l2.l_orderkey`. `create_nestloop_path` (pathnode.c:2703) drops
a clause only when its `rinfo_serial` is one the parameterised inner
enforces. The inner enforces the `l1`–`l2` clause, so the `orders`–`l2`
clause survives as a redundant Join Filter.

goopg differed in two places:

1. **The semi join was not reduced.** `buildJoinRelRestrictList` reduced
   one clause per class only for an inner join (M0146-0022).
   `dropRedundantSemiDerived` then removed the semijoin-derived
   `o_orderkey = l2.l_orderkey` beside the written clause — the opposite of
   PG's pick.
2. **The reduction was off for Q21's whole problem.** `ecReduce` was set
   only when every special join is SEMI. An ANTI join was treated as
   null-extending, because its qual (`l3.l_orderkey = l1.l_orderkey`) was
   unioned into the class, which put `l3` members into it.

## Change

- `buildRestrictInfosExcluding` (`joinrestrict.go`) keeps clauses that
  touch an ANTI join's RHS (`antiRighthands`) out of the equivalence
  classes. In PG an ANTI qual is outerjoin-delayed and never a class member.
  Nothing outside an ANTI join can name its RHS, so every such clause is the
  join's own qual.
- `ecReduce` is `onlySemiOrAntiJoins`. Neither SEMI nor ANTI null-extends a
  relation visible above it, and with ANTI quals out of the classes no class
  member comes from an ANTI RHS.
- `buildJoinRelRestrictList` reduces a SEMI join like an inner join. It runs
  `reduceEquivClassJoinClauses` over every applicable clause first
  (`clausesForAll`, semijoin-derived ones included), then
  `dropRedundantSemiDerived`. The derived clause the reduction chose is
  therefore kept, and only a derived clause left beside a written one is
  removed. The outer-join filter half moved into `nullableSideFilters`
  unchanged.
- Test: `TestSemiJoinRestrictListTakesFirstMemberClause`, on Q21's shape:
  - the ANTI qual has no class id;
  - the SEMI join applies `o.k = l2.k`;
  - the ANTI join keeps its own qual.

## Measured

Captures in `analysis/m0146/m0146-0200/`, compared with the M0146-0199
captures:

- **TPC-H:** match 15 → 16 (Q21 MATCH). No other plan changed.
- **TPC-DS SF0.25 and SF1:** plan text byte-identical (84/99, 71/99).

The acceptance arm executed every TPC-H query, Q21 included, and all values
are identical (24 MATCH).

## Residual

The selectivity side (`selectivityClauses` → `oneClausePerEquivClass`) still
picks a class's clause by "explicit beats inferred". PG's estimate reads the
same restrictlist clause the reduction picks. The two clauses equate the same
class, so the estimates agree today.
