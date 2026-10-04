# M0146-0005dz — an outer-join clause made redundant by a derived constant is removed

Status: done (2026-10-04). Parent: M0146-0005. Filed by slice 115.

## The divergence

TPC-DS Q78 joins three grouped CTEs:
`ss LEFT JOIN ws ON ws_sold_year = ss_sold_year AND ws_item_sk = ss_item_sk AND ws_customer_sk = ss_customer_sk LEFT JOIN cs ON … WHERE ss_sold_year = 2000`.

| | Merge Left Join keys |
|---|---|
| PG 18.3 | customer and item |
| goopg before | customer, year and item |

## PG's mechanism

`reconsider_outer_join_clauses` (`./postgres/src/backend/optimizer/path/equivclass.c`)
handles an outer-join clause `OUTERVAR = INNERVAR` whose OUTERVAR is
equated to a constant.

1. **Push the constant.** It pushes `INNERVAR = CONSTANT` into the nullable
   side.
2. **Remove the clause.** It removes the clause from the join. Every pair
   that can still meet has both sides equal to the constant, so the clause
   tests nothing.
3. **Keep the join connected.** It throws back a constant-TRUE clause with
   the removed clause's `required_relids`, only so the join is not treated
   as clauseless.

## Change

goopg already derived the constant on two routes. It now also removes the
clause on both.

- **Grouped items (Q78's route).** The AST push of M0146-0005bn
  (`subquerypushqual_ast.go`) moves `ss_sold_year = 2000` into the grouped
  bodies.
  - The push follows the LEFT join's ON equality into the nullable
    partner.
  - `pushGroupedItemConjunct` now reports which ON equalities carried the
    constant to a nullable partner.
  - Once the push is recorded, `dropRedundantOnConjuncts` strips them from
    their join's ON clause.
- **Base relations (the join-search seam).** `deriveOuterLinkConstants`
  (`joinsearchseam.go`) reports the ON conjuncts behind each derived
  `null = const`, and the seam leaves them out of the search's clause list.
- **No dummy TRUE.** On both routes a clause is dropped only while its join
  keeps another conjunct. That does the job of PG's constant-TRUE stand-in.

The legacy syntactic-tree route (`deriveConstAcrossJoinEquality`) is
unchanged. No test reaches it with an outer link, so a drop there would be
unverified (ledgered).

## Effect

- **Q78.** Both Merge Left Joins key on customer and item at both scales,
  as in PG. The row estimates did not move (779 / 1403 at SF0.25), so the
  year clause was already costed at selectivity 1.
- **Fire set.** Only Q78 fired. Matches and categories are flat at both
  scales, because Q78's remaining categories have other causes (join
  order, the store\_returns anti join's estimate).
- **Other gates.**
  - The sweep passed 96/96.
  - The TPC-H arm matched 24/24, and tpch-spotcheck passed.
  - ea-ratchet passed. Its "Q92 fixed" line predates this change.
  - Regress A/B over 18 planner cases changed only nondeterministic
    parallel NOTICE order.

Test: `TestOuterJoinClauseRedundantAfterConstantDerivation` fails on HEAD.
It covers both routes: grouped CTE bodies and base relations.

- The join condition must not mention the year, and both inputs must carry
  `y = 2000`. This is PG 18.3's plan on the same data, with row estimates
  1500 and 566.
- The values must equal the query whose ON writes `ws.y + 0 = ss.y`, from
  which nothing is derived.

## Not done (ledgered)

- **The legacy route.** `deriveConstAcrossJoinEquality`
  (`inner_join_qual_pushdown.go`) plants the constant but keeps the clause.
- **Inner joins and WHERE equalities.** Between two items both pinned to
  one constant, the equality still stays in the join. PG's EC machinery
  generates no join clause for an EC that has a constant.
