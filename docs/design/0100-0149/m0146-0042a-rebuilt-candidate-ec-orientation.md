# M0146-0042a — a rebuilt search candidate prints PG's equality orientation

Status: done 2026-10-05 (`36487b6d1`). Parent: M0146-0042 (EXPLAIN
text-identity). Evidence: `analysis/m0146/m0146-0042a/`.

## PG mechanism

Every inner-join equality is re-derived from its equivalence class by
`create_join_clause` (equivclass.c). That function returns an existing
clause in whichever orientation it was first created
(`ec_search_clause_for_ems` matches either order), so creation order fixes
the printed text:

- an index key column's own rel first;
- the parameterising rel first for an indexed rel's other clauses;
- otherwise FROM order.

A nested loop's Join Filter and a parameterised scan's Filter print the
clause as it is. Only Hash and Merge Cond are switched outer-first.

## goopg

M0146-0005de ported the rules (`orientECJoinClauses`) and applies them to
the committed searched tree and the conjuncts (`applyECOrientation`, at the
seam).

M0146-0027 then added the upper stages' `is_sorted` iteration: a
non-winning candidate of the search's upper rel is rebuilt later by
`searchedBoundaryRebuild` (for example, a Gather Merge → Sort input for a
GroupAggregate). That lowering makes fresh clause copies after the seam has
run:

- the EC-reduced join clause, which `equivClassJoinClause` flips
  outer-first;
- a parameterised scan's rebased Filter.

So those clauses printed goopg's order. TPC-DS SF1 Q17/Q25/Q29 printed
`item.i_item_sk = catalog_sales.cs_item_sk` and `store_sales.ss_customer_sk
= cs_bill_customer_sk`, where PG prints them the other way round.

## Change

- The seam records its orientation on the search's upper rel
  (`RelOptInfo.ecWant`).
- `searchedBoundaryRebuild` applies it to the rebuilt tree.
- `applyECOrientation` sets the wanted order rather than toggling, so clause
  objects shared with the committed tree stay correct.

The task's first hypothesis, that inferred equalities miss the orientation
map, was refuted: the seam already orients them.
`TestInferredJoinEqualityPrintsInECOrder` pins that behaviour.

## Result

- TPC-DS SF1 Q17/Q25/Q29 match PG 18.3's text, costs aside. SF1
  text-identical goes 17 → 20.
- SF0.25 Q64's Join Filter matches PG.
- CATEGORIES-EXCL-MATCH is unchanged at both scales: this is a text-only
  fix, and the three queries were already structural matches.

## Open

- No fixture reproduces the rebuild path together with an EC-reduced
  Join Filter, so the witness is the SF1 capture (ledgered).
- M0146-0042b: a join's EC-derived clauses print after its other quals.
