# M0148\-0001c3a13 — row IN lists as per\-column comparisons

Status: landed 2026\-11\-14. Parent: M0148\-0001c3a6.

## Problem

- create\_index's witness:
  `select * from tenk1 where (thousand, tenthous) in ((1,1001), (null,null))`.
  - PG 18.3: `Index Scan … Index Cond: ((thousand = 1) AND (tenthous = 1001))`.
  - goopg: `Seq Scan … Filter: (ROW(thousand, tenthous) = ANY (ROW(1, 1001),
    ROW(NULL, NULL)))`.
- goopg kept a row IN list as one InExpr over rows. The planner could not
  see per\-column equalities, so no index qual, no BitmapOr, and no constant
  folding.
- It also gave wrong values. The InExpr evaluator compared rows two\-valued,
  so `(1, 3) IN ((1, 2), (NULL, NULL))` was `false` instead of NULL, and the
  NOT IN form was `true` instead of NULL.

## PG mechanism

- `transformAExprIn` \(parse\_expr.c\) builds a ScalarArrayOpExpr only when
  the list items share a common type with an array type. Row items have
  type RECORD, which is excluded, so a row IN always takes the "hard way".
- Each item becomes `make_row_comparison_op` of the IN's operator.
  - For `=`, that is an AND of per\-column `=`.
  - For `<>` \(NOT IN\), it is an OR of per\-column `<>`.
  - The items are combined with OR \(IN\) or AND \(NOT IN\), in list order.
- `eval_const_expressions` folds `col = NULL` \(a strict operator\) to a
  NULL constant.
- `canonicalize_qual` / `find_duplicate_ors` with is\_check = false treats a
  NULL constant as FALSE. An AND containing one becomes FALSE, and an OR
  drops FALSE arms. So the `(null, null)` item vanishes from a WHERE qual.

## goopg implementation

- `rowInListAsBooleanTree` \(internal/optimizer/planner.go\) is called from
  `planInExpr`. It builds the parser\-level AND / OR tree, then resolves it.
  - It applies to the IN \(val\_list\) syntax only: not `= ANY`, not a
    subquery, and not a quantified list.
  - Every item and the operand must be row constructors. Both spellings
    are accepted via `rowConstructorElems`: the implicit `(a, b)`
    \(parser.RowExpr\) and the explicit `ROW(a, b)` \(a call named row\).
  - A width mismatch raises PG's 42601 "unequal number of entries in row
    expressions".
- `oneElementInAsComparison` now leaves a one\-item row IN to the same
  expansion, because a one\-item list is make\_row\_comparison\_op too.
- The existing `FoldConstants` and `reduceQualConstArms` \(foldconst.go,
  M0148\-0001c3a2\) supply the folding and the NULL\-arm drop. No new pass
  was needed.
- EXPLAIN: a NULL arm of an AND / OR prints `NULL::boolean`, as PG's
  get\_const\_expr prints a boolean NULL Const
  \(`(((thousand = 1) AND (tenthous = 1001)) OR NULL::boolean)`\).

## Verification

- A probe of 20 statements against scratch PG 18.3 \(port 5545\) matches
  plans and values, except for the two items under "Not covered".
- `TestRowInListExpandsToComparisons` pins four plans and three value
  checks. It fails 6 ways with the expansion disabled.
- Regress create\_index 2483→2466: the witness hunk is gone.

## Not covered

- A row operand whose list mixes rows and scalars \(`(1,2) in ((1,2),(3))`\)
  keeps the old InExpr path. PG raises "operator does not exist: record =
  integer".
- EXPLAIN VERBOSE never prints a `Result` node's `Output:` line \(general,
  not specific to row IN\).
- A target list over index columns does not make an Index Only Scan
  \(the index\-only width item already parked in M0148\-0001c3a11\).
- Nested row elements keep the old path \(make\_op over rows is not
  reproduced\).
