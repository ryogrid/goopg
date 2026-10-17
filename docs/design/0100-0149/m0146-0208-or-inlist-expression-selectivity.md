# M0146-0208 — an OR arm `expr IN (constants)` is priced by scalararraysel, not the whole-OR default

Status: done 2026-10-16 — TPC-DS Q45 matches PG at SF0.25 and SF1.

## Problem

TPC-DS Q45 filters its join with

```
substr(ca_zip, 1, 5) IN ('85669', …9 constants…) OR i_item_id IN (SELECT i_item_id FROM item WHERE …)
```

The clause references `customer_address` and `item`, so it is a join clause. It is the Nested Loop's Join Filter in
both engines. PG estimates it with `clauselist_selectivity_or`
(`postgres/src/backend/optimizer/path/clausesel.c:359`), arm by arm:

- **The substr arm.** It covers one relation, so `treat_as_join_clause` treats it as a restriction.
  `scalararraysel` (`postgres/src/backend/utils/adt/selfuncs.c:1824`) runs `eqsel` → `var_eq_const`
  (`selfuncs.c:303`) per element. The expression has no statistics, so each element is `1.0 /
  get_variable_numdistinct()` (`selfuncs.c:455`), that is 1/`DEFAULT_NUM_DISTINCT` = 1/200
  (`postgres/src/include/utils/selfuncs.h:52`). The disjoint sum is 9 × 0.005 = 0.045.
- **The hashed-SubPlan arm.** It has no estimator and falls to `boolvarsel`'s 0.5 (`clausesel.c:956`).
- **The OR.** 0.045 + 0.5 − 0.0225 = 0.5225. PG keeps 0.524 of the rows at SF1 (292 → 153) and 0.53 at SF0.25.

goopg's `orInListSelectivity` (joinselectivity.go) priced an OR arm's IN-list only over a bare column. It declined
the `substr(…)` operand to the whole-OR default of 0.5, so the OR came to 0.75 (283 → 212). The larger group input made
goopg's Partial/Finalize GroupAggregate cheaper than PG's plain GroupAggregate over Sort over Gather. That was the
first divergence (aggregation-strategy, depth 1).

## Change

- `orInListExprSelectivity` handles an operand that is an expression over **one** search relation. Every ColumnRef in
  it must resolve through `relPosForSource` to the same rel, and the elements must be constants with an equality
  `ANY`. It returns the restriction path's own `inListExprSelectivity` (selectivity.go, M0145-0008g), which merges
  `eqOpSelectivity`'s no-statistics 1/200 per element exactly as scalararraysel does. The OR-arm and restriction
  siblings therefore agree by construction. The result is flagged as a guess, as `var_eq_const`'s no-statistics
  branch is.
- Unchanged: the hashed-SubPlan arm keeps 0.5, which is PG's `boolvarsel` default. An operand spanning relations and a
  non-equality `ANY` over an expression still decline to 0.5 (ledgered).
- Test: `TestOrInListExprSelectivity`. It covers the 9 × 1/200 arm, sibling equality with `inListExprSelectivity`, the
  Q45 OR = 0.5225, and the two declines.

## Measured

Captures in `analysis/m0146/m0146-0208/`. The fire-set A/B ran HEAD `c78b5d6a8` against the staged change:

- **TPC-DS SF0.25:** match 84 → 85 (Q45). CATEGORIES-EXCL-MATCH aggregation-strategy 1 → 0, join-order 13 → 12,
  join-method 9 → 8, parameterisation 6 → 5, sort-strategy 6 → 5, parallelism 5 → 4.
- **TPC-DS SF1:** match 71 → 72 (Q45). aggregation-strategy 6 → 5.
- **Q15**, the other changed plan, stays MATCH. Its estimate moves to PG's: 55 rows vs PG 54, was 100.
- **TPC-H:** plan text byte-identical, 20/22.
- **Values:** SF0.25 sweep PASS=99 (Q15 88 rows and Q45 3 rows ck-verified); TPC-H acceptance arm 24 MATCH.
