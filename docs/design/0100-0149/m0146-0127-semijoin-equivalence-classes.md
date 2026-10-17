# M0146-0127 — a semijoin's equality joins the equivalence classes

Status: done 2026-10-09 (4eb16d5f1). Parent: M0146-0012a.

## Problem

TPC-DS Q14's three `cross_items` branches each read

```sql
FROM store_sales, item, date_dim
WHERE ss_item_sk IN (SELECT ss_item_sk FROM cross_items)
  AND ss_item_sk = i_item_sk AND ss_sold_date_sk = d_date_sk ...
```

PG 18.3 plans them as follows:

```
HashAggregate(cross_items) -> Index Scan item_pkey (i_item_sk = cross_items.ss_item_sk)
  -> Index Scan store_sales_pkey (ss_item_sk = item.i_item_sk)
```

That plan costs 27.68. goopg probed `store_sales` straight from
`cross_items` with a bitmap scan, costing 190.

A `GOOPG_PGSHAPED_DP_TRACE` capture on the private SF0.25 clone showed the
cause: `decline reason=no-join-clause pair={cross_items} | {item}`. PG's
order was never built.

## PG behaviour

- `distribute_qual_to_rels` (initsplan.c) gives a SEMI join's qual no
  nullable side. The qual is pushed down, `maybe_equivalence` holds, and
  `process_equivalence` merges it into an EquivalenceClass.
- The class `{ss_item_sk, i_item_sk, cross_items.ss_item_sk}` then lets
  `generate_join_implied_equalities` derive `i_item_sk =
  cross_items.ss_item_sk`.
- `join_is_legal`'s unique-ification arm joins the unique-ified RHS to
  `item`.
- The `store_sales_pkey` probe parameterised by `item` is priced with
  `get_loop_count` = 18000, item's base rows, which makes it cheap.
- `generate_base_implied_equalities` adds every class member's Var to
  the target lists up to the class's relids. A PG semijoin therefore emits
  any RHS column the class still needs above it.

## goopg constraints

- The seam's transitive closure (`inferTransitiveEqualities`,
  joinsearchseam.go) runs over the WHERE conjuncts before the semijoin
  quals join the list.
- goopg's semi and anti joins emit their preserved side only
  (`Join.Output`). A derived clause that reads an RHS column cannot be
  evaluated above a semijoin that consumed that RHS.
- The same joinrel can be built either way. `{cross_items, item,
  store_sales}` may come from a unique-ified join, where `cross_items`'s
  columns exist, or from a semijoin, where they do not. Availability is a
  property of the path, not of the relids.

## Change

- **Derivation.** `semiRHSImpliedEqualities` (semiecderive.go) closes the
  seam's inner conjuncts together with each single-relation SEMI join's
  equalities. It keeps the derived `RHS = outside` equalities and records
  each one's RHS operand.
  - Members on an outer join's nullable side are excluded, as are ANTI
    joins.
  - The derived clauses join the conjunct list, in closure order. The
    problem carries the operand map (`semiRHSOperand`), and
    `relfromjoinlist.go` tags each restrictInfo with the RHS relation in
    item coordinates (`onlyBesideRel`).
- **Join clauses.** `clausesFor` applies a tagged clause only where one
  input is exactly the RHS. There it serves as the unique-ified join's
  clause, or sits beside the semijoin's own qual.
  - `dropRedundantSemiDerived` keeps it only when no explicit clause of
    the same class is already selected, giving one clause per class.
    Without this rule, TPC-H Q21 printed a redundant
    `l2.l_orderkey = orders.o_orderkey`.
  - `flipped` copies the tag.
- **Parameterised probes.** `paramOuterKeepsRHS` / `pathKeepsRels` check
  every parameterised inner whose required outer includes such an RHS. The
  outer path must contain no semi or anti join that consumed it. The check
  runs in `addNLIPaths` and `addPartialNestLoopPaths`, the two loops over
  `CheapestParameterized`.

## Verification

- **Test.** `TestSemiJoinEqualityJoinsEquivalenceClass` covers the
  derivation, the nullable and ANTI exclusions, the `clausesFor` placement
  and dedup, and `pathKeepsRels`. It fails at HEAD.
- **Probe.** Q14's first branch, with `cross_items` MATERIALIZED, now joins
  in PG's order on the private clone.
- **TPC-DS fire set.** Q14 was executed in both arms with identical
  results.
  - Q14 loses its parameterisation divergence at both scales:
    parameterisation goes 21 → 20 at SF0.25 and 28 → 27 at SF1.
  - No other query fires.
- **ea-ratchet.** Findings go 7 → 1. The six Q14 `cross_items` Nested Loop
  misestimates are gone.
- **TPC-H.** Plans byte-identical.
- **Regress A/B** (32 cases).
  - `subselect`'s two `IN (VALUES …)` EXPLAINs diverge from PG either way,
    because PG 18 rewrites them to `= ANY` (ledgered under M0146-0005dk).
    They now take a different shape with a redundant class equality.
  - `join` and `stats_ext` show only their known row-order flips.
- **Gates.** units, TPC-H spotcheck, acceptance arm, fire set, SF0.25 sweep
  and ea-ratchet all PASS.

## Remaining (M0146-0128 and ledger)

- **One clause per class.** At an inner join above a unique-ified RHS,
  goopg applies both the WHERE equality and the semijoin qual. Q14 shows
  `Join Filter: (store_sales.ss_item_sk = cross_items.ss_item_sk)`, which
  PG does not have. `ecReduce` is off whenever any special join exists,
  while M0146-0022's stated rule is "no special join can null-extend a
  class member", and a SEMI join null-extends nothing. Filed as
  M0146-0128.
- **`item` scan type.** Q14 probes `item_pkey` with a Bitmap Heap Scan
  where PG uses a plain Index Scan, at cost 8.30 for one row.
- **Ledgered.**
  - A multi-relation semijoin RHS.
  - Derived equalities between two members outside the RHS, or two inside
    it.
  - PG's emission of RHS columns above a semijoin.
