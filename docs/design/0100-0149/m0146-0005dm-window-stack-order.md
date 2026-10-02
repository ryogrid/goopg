# M0146-0005dm — stacked WindowAggs in select_active_windows order

Status: done (2026-10-03). Parent: M0146-0005 (banner item 3).

## The PG behaviour

`grouping_planner` stacks one WindowAgg per active window. The order comes
from `select_active_windows` (planner.c): a sort of the windows by
`common_prefix_cmp` over each window's `uniqueOrder`, which is its
partition clauses followed by those order clauses not already present.
The first window in the result is the lowest WindowAgg, and
`name_active_windows` then names the unnamed windows `w1`, `w2`, … in that
order.

`common_prefix_cmp` walks the two lists in step:

- the larger `tleSortGroupRef` sorts first, then the larger sortop (DESC,
  whose `>` has the higher OID), then NULLS FIRST;
- if one list is a prefix of the other, the longer one sorts first, so the
  stronger sort is applied before the weaker window can reuse it.

`tleSortGroupRef`s are handed out by parse analysis on first use, in the
order of `transformSelectStmt`:

1. ORDER BY;
2. GROUP BY;
3. DISTINCT / DISTINCT ON;
4. the window definitions: the WINDOW clause's named windows, then each
   inline OVER spec. `transformWindowDefinitions` transforms a window's
   **ORDER BY before its PARTITION BY**, so order items take their refs
   first.

A partition item that is also an order item inherits that item's sort
operator: `transformGroupClauseExpr` is handed the window's
`orderClause`.

## What goopg did

`buildWindowStage` stacked the spec groups in the order their calls first
appear and named them before stacking. On TPC-DS Q47, Q57 and Q49 the
partition-only window therefore sat below the ORDER BY window, the reverse
of PG, and `w1` / `w2` were swapped. This is cost-neutral but visible in
the plan's shape.

## What landed

- `orderWindowDefsLikePG` (window_order.go) reproduces the ref assignment
  and `common_prefix_cmp` over the spec groups' window definitions, using
  a stable sort.
- `buildWindowStage` reorders its groups before naming them.
- Expressions are matched by `parserExprKey`. Two spellings PG resolves to
  one target entry (qualified and unqualified) count as two, which can
  change only the order, never the results.

## Verification

- **Scratch probes against PG 18.3.** Five window shapes give identical
  EXPLAIN text:
  - a grouped pair (Q47's shape);
  - a common prefix;
  - order-first refs;
  - DESC over ASC under a statement ORDER BY;
  - named WINDOW clauses.

  `TestOrderWindowDefsLikePG` pins four of them.
- **Regress A/B against HEAD.** `window` shrinks 4447→4348 lines: several
  window results now come out in PG's row order, and window names match.
  Five other suites are byte-identical.
- **TPC-DS fire set** (Q47, Q49, Q57 at both scales; values PASS):
  - Q47's stack and names match PG;
  - CATEGORIES-EXCL-MATCH `rendering` 11→10 (SF0.25) and 14→13 (SF1);
  - aligned PG lines +4 at each scale.
- **Gates:** units, spotcheck, sweep 96/96, TPC-H arm, ea-ratchet, fire
  set.

## Found on the way

**M0146-0048 (S2, filed, not worked):** two windows that differ only in
NULLS FIRST/LAST share one WindowAgg, because `windowSpecKey` ignores
the nulls ordering. `rank() OVER (ORDER BY x)` then ranks under the
NULLS FIRST order: goopg returns 2, 3 where PG returns 1, 2.

## Residuals (ledgered)

- Expression identity is the parser key, not PG's resolved target-entry
  equality.
- The sortop rule assumes `>` outranks `<` by OID, which holds for the
  built-in btree types. A user-defined opclass could order differently.
- `optimize_window_clauses` (PG 16+, merging windows whose frames are
  equivalent for their functions) is not consulted before the sort.
