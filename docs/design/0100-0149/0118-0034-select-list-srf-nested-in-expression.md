# 0118-0034 — SELECT-list set-returning function nested in an expression

Status: accepted
Milestone: M0118-0008 (enabler) / general planner correctness
Date: 2026-06-22

## Summary

Fixes a silent row-dropping correctness bug: a set-returning function (SRF)
nested inside a larger SELECT-list scalar expression — e.g.
`generate_series(1,1000) % 4` — collapsed to a **single** row holding the SRF's
start value instead of expanding to one row per series element. Concretely,

```sql
CREATE TABLE b (a_id int);
INSERT INTO b SELECT generate_series(1,1000) % 4;   -- inserted 1 row, not 1000
SELECT * FROM b WHERE a_id = 3;                      -- (0 rows)
```

PostgreSQL evaluates the SRF first (1000 rows) and applies `% 4` per row. goopg
only expanded an SRF that was the **bare** target expression (optionally wrapped
in a single cast); an SRF buried in any other expression fell through to the
scalar `generate_series` fallback, which returns just `start`.

This is the root blocker for the `alter-table-1` / `alter-table-2` isolation
specs (their setup is `INSERT INTO b SELECT generate_series(1,1000) % 4`), and a
general correctness bug affecting any `SELECT <expr-over-srf()>`.

## Root cause

`buildSelectSrfProjectSet` (`internal/planner/planner.go`) detected SELECT-list
SRFs only when the target expression *was* a recognized SRF `FuncCall` (after
unwrapping at most one `CastExpr`). For `generate_series(1,1000) % 4` the target
is a `BinaryOp` whose left operand is the SRF, so detection skipped it; with no
SRF detected, no `ProjectSet` was planned and the SRF resolved to its scalar
form (`internal/planner/planner.go` scalar fallback → returns `start`).

## Fix

Detect an SRF **nested** inside a target expression and rewrite it into a
*wrapper* that applies the enclosing scalar expression to each expanded element.

### Planner (`internal/planner/planner.go`)

- New helpers:
  - `isNestedSRFName` — currently `generate_series` only (bare and FROM-clause
    SRFs keep their existing, well-tested paths; bounded blast radius).
  - `findFirstNestedSRF(Expr) *FuncCall` — DFS for the first SRF call in a
    resolved expression tree.
  - `replaceExprNode(e, target, repl) Expr` — rebuilds the tree with the
    pointer-matched node replaced. Both walk the same node kinds (`FuncCall`,
    `BinaryOp`, `UnaryOp`, `CastExpr`, `CollateExpr`, `CaseExpr`) and mirror
    `shiftColumnRefsBy`'s rebuild discipline.
- `buildSelectSrfProjectSet`: a new pre-scan resolves each non-bare-SRF target,
  locates a nested generate_series, assigns it a **temp eval-row slot**
  (`ChildWidth + k`, beyond the visible output columns), and records a wrapper =
  the resolved target with the SRF `FuncCall` replaced by a
  `ColumnRef{Index: slot}`. A nested-SRF-only query now still builds a
  `ProjectSet` (the early-return condition includes wrapped SRFs).

### Plan node (`internal/planner/plan.go`)

- `SrfCol`/`UnnestCol` gain `Wrapped bool` — when set, the executor writes the
  raw per-step SRF value into the eval row (temp slot at `ColIdx`), not the
  output row.
- `ProjectSet` gains `Wrappers []SrfWrapper{OutCol, Expr}`, `ChildWidth`, and
  `EvalRowWidth` (`ChildWidth + #wrapped`).

### Executor (`internal/executor/operators_project_set.go`)

In the zip loop, when wrappers exist, build a per-step `evalRow` of width
`EvalRowWidth`: the child row occupies `[0:ChildWidth)`, each wrapped SRF's
per-step value (or NULL when zip-padding a shorter series) goes into its temp
slot. After the bare SRF / unnest / user-SRF columns are placed, each wrapper is
evaluated against `evalRow` and written to `outRow[OutCol]`. Non-wrapped paths
are byte-for-byte unchanged (no `evalRow` is allocated when `Wrappers` is empty).

## Correctness of the eval-row indexing

A wrapper's non-SRF `ColumnRef`s keep their child-relative indices (`< ChildWidth`)
and resolve correctly because `evalRow`'s prefix is the child row; the
substituted SRF `ColumnRef` resolves to the appended temp slot. The plan-time
`ChildWidth` (= `len(child.Output())`) is the single source of truth shared by
slot assignment and eval-row construction.

## Scope / non-goals

- Only `generate_series` is expanded when nested; nested `unnest` / user-SETOF
  functions and multiple SRFs in one target expression keep current behavior
  (the pre-scan takes the first SRF and only when arity is valid). The `Wrapped`
  flag on `UnnestCol` is reserved for a future extension. *(Superseded by
  M0149-0001h1a78, below: unnest, regexp_matches, user SETOF routines and
  several SRFs in one expression are now expanded.)*
- WAL / storage / visibility untouched.

## Tests / gates

- `TestSelectListSRFInsideExpression` (`internal/executor/srf_in_expr_test.go`):
  `generate_series(1,10) % 4`, `+ 100`, three-arg `(2,10,2) * 10`, and a bare
  `generate_series(1,4)` regression.
- `go test ./internal/planner/... ./internal/executor/...` PASS.
- Regress-port parity suite (`TestPort_RegressSuite`) PASS.
- pgbench TPC-B smoke (pre-commit hook) 0-failed.

## Follow-up

`alter-table-1` / `alter-table-2` are still deferred: they additionally need FK
`... NOT VALID` parsing, `ALTER TABLE ... VALIDATE CONSTRAINT`, and the
ShareUpdateExclusive/ShareRowExclusive lock semantics. This change removes their
data-setup blocker.

## pg\_typeof over a set\-returning call \(M0149\-0001h1a64\)

`pg_typeof` is folded at plan time to its argument's type name, which used to drop a nested
`generate_series` and return one row. `foldPgTypeof` now keeps such an argument as a second argument that
the executor never reads, so `findFirstNestedSRF` still finds the call and the row count is PG's.
Only generate\_series is expanded when nested; other set\-returning calls, EXPLAIN's shape for these plans and
PG's errors for set\-returning calls inside CASE and COALESCE are M0149\-0001h1a78.


## unnest, several SRFs in one expression, and CASE/COALESCE errors \(M0149\-0001h1a78\)

- `isNestedSRFName` now covers `unnest` as well as `generate_series`. The ProjectSet pre\-scan uses
  `findFirstNestedSRFWith` with a wider test that also matches `regexp_matches` and the catalog's SETOF sql and
  plpgsql routines, the same set the bare\-target scan expands. `nestedSRFArityOK` checks the arity per function:
  2 to 4 arguments for generate\_series, exactly one for unnest \(the multi\-array form is FROM\-only in PG too\),
  and 2 or 3 for regexp\_matches. A user routine is matched on its argument count.
- A wrapped SRF of each kind is its usual column with `Wrapped: true`: `UnnestCol`, `RegexpMatchesCol` or
  `UserSrfCol`. The executor writes the per\-step value into the eval row instead of the output row.
- The pre\-scan replaces every nested SRF in a target, not only the first. Each SRF gets its own eval\-row slot,
  so `unnest(array[1,2]) * generate_series(1,3)` runs both in lockstep like PG's ProjectSet: the result is 1, 4 and
  NULL, with the shorter SRF padded with NULL.
- A wrapped unnest takes its element cast from `unnestElementCastType`, the helper the bare unnest uses as well.
- Column names follow FigureColname. `figureColnameInternal` \(internal/parser/figurecolname.go\) gained PG's
  A\_Indirection arm: a subscript has no field name, so the subscripted expression names the column, as in
  `(regexp_matches(s, p))[1]` → regexp\_matches. targetMeta and exprOutputName use it for subscripted targets, so
  `(array[1,2])[1]` is `array`, where goopg used to print `int4` or `?column?`.
- A slot ColumnRef takes `exprType` of its SRF, so `generate_series(1,2) + 1` is `integer`, as in PG. The slot
  used to be fixed at `int8`.
- The analyzer applies PG's rules from parse\_expr.c:
  - transformCaseExpr: a WHEN condition that returns a set fails coerce\_to\_boolean with 42804. Any other
    set\-returning call in a CASE raises 0A000 `set-returning functions are not allowed in CASE`.
  - transformCoalesceExpr raises the same 0A000 error for COALESCE.
  - The error is reported at the last SRF transformed \(p\_last\_srf\), with PG's LATERAL hint.
- EXPLAIN for these plans is M0149\-0001h1a78a, described in the next section.
- Found along the way and filed separately:
  - `(array[1,2])[1] + 1` fails with `operator + requires numeric operands` \(M0149\-0001h1a78b\).
  - string\_to\_array has no executor implementation \(M0149\-0001h1a78c\).

## EXPLAIN estimates for a SELECT\-list ProjectSet \(M0149\-0001h1a78a\)

goopg evaluates the SRFs and the expressions over them in one ProjectSet node. When an SRF sits inside an
expression, PG splits the target list instead \(split\_pathtarget\_at\_srfs\): a ProjectSet for the SRFs and a
Result above it for the expressions. internal/optimizer/projectset\_cost.go computes PG's numbers for that pair:

- `ProjectSetOwnCost` is create\_set\_projection\_path:
  - rows are the input's rows times the largest SRF estimate \(expression\_returns\_set\_rows\). generate\_series
    takes its support function's count, else its prorows of 1000. unnest takes estimate\_array\_length of its
    argument: the argument count of an ARRAY constructor, the element count of an array literal, else 10.
    regexp\_matches takes 10, and a user routine its ROWS clause, else 1000.
  - total cost is the input's total, plus cpu\_tuple\_cost and the target's per\-tuple cost for each input row,
    plus half of cpu\_tuple\_cost for each added row.
  - the target is the SRF calls, plus the other expressions when no Result is planned above.
- The Result above is create\_projection\_path: cpu\_tuple\_cost plus the whole target's cost for each row.
  The target still holds the SRF calls inside its expressions, so they are charged a second time, as in PG.
  `ProjectSetNeedsResult` decides whether there is a Result: there is one when some SRF is wrapped, or when an
  unnest has an explicit cast.
- An ARRAY constructor and a subscript are free in cost\_qual\_eval \(ArrayExpr, SubscriptingRef\). goopg resolves
  them to `array_construct` and `array_subscript` calls, so `targetExprOps` takes those calls back out.
- DeriveLegacyDisplayCost and EstimateRows report the pair's top, which is what a parent is priced over.
  EXPLAIN prints the ProjectSet's own estimate on its own line \(`explainNodeCostFields`\). The synthesised Result
  prints the pair's top: at the top level through `emitRootSRFResult`, and under a parent through the existing
  child loop, whose Result line now has a real startup cost.
- A Project that returns a ProjectSet's columns unchanged is no projection to PG, so it is priced as the
  ProjectSet in DeriveLegacyDisplayCost and in subplanLeafSubpathCost. Before this change, a derived table
  `select generate_series(1,10) x` charged cpu\_tuple\_cost per row twice.
- Casts are still priced at zero, so `unnest(...)::text` comes out 0.01 low.

## VERBOSE output for a SELECT\-list ProjectSet \(M0149\-0001h1a78d\)

internal/executor/explain\_projectset.go rebuilds the two targetlists of split\_pathtarget\_at\_srfs from goopg's
single ProjectSet node:

- **ProjectSet.** Its Output is the deparsed SRF calls in SELECT\-list order, a wrapper's slots at its column's
  position, followed by the input columns the Result still reads. Without a Result, it is the whole SELECT list.
- **Result.** Its Output is the SELECT list. Every reference to a ProjectSet column is a non\-Var OUTER\_VAR
  referent, so ruleutils wraps it in parentheses: a slot becomes `displayColumn(slot, "(call)")`, which gives
  `((generate_series(1, 2)) + 1)`. The root Result line \(`emitRootSRFResult`\) and the Result printed under a parent
  both print this list.
- **Pass\-through nodes and Sort Keys.**
  - A Sort or Limit above the pair, including a chain such as a Limit over a Sort, references the pair's top.
    Non\-Var entries are parenthesised, and Consts stay bare, because setrefs leaves them in the parent's tlist.
  - The Sort Key chase returns the parenthesised Result entry for a wrapped column.
- **New deparse arms in formatExprQual:**
  - `array_construct` prints as PG's folded array Const when every element is a constant of one kind
    \(`'{1,2}'::integer[]`), and as `ARRAY[a, b]` otherwise.
  - `array_subscript` prints as a SubscriptingRef, `(container)[i]`, with no parentheses around a plain column.
- **Width.** The legacy display cost prints width 0 for a node with an empty targetlist, such as the input Result
  of a FROM\-less SELECT. The planner's `tupleWidth` floor of 1 is unchanged.
- **Not done:**
  - A Values Scan's columns print under their SQL alias \(`x`\), where PG prints `"*VALUES*".column1`.
  - A stamped Seq Scan feeding `count(*)` still prints `width=4`, where PG prints `width=0`.
