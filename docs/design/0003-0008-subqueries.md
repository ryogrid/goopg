# Subqueries (Milestone 0003)

| Field      | Value                                                  |
| ---------- | ------------------------------------------------------ |
| Status     | draft                                                  |
| Date       | 2026-04-28                                             |
| Milestone  | 0003 — HammerDB TPC-H Workload                         |
| Refines    | [root-0010-parser.md](root-0010-parser.md), [root-0011-planner.md](root-0011-planner.md), [root-0012-executor.md](root-0012-executor.md) |
| Supersedes | —                                                      |

## Problem

Many TPC-H queries gate predicates on the result of a sub-SELECT.
The simplest shape is **uncorrelated scalar**:

- Q15: `total_revenue = (SELECT max(total_revenue) FROM revenue$myposition)`
- Q17 / Q22 use similar `> (SELECT avg(...))` patterns.

This loop adds the scalar form. `IN` / `NOT IN` / `EXISTS` /
correlated subqueries (parameter pull-up) are deferred —
they need different planner/executor paths.

## Upstream reference

- `postgres/src/backend/parser/gram.y` — `select_with_parens`
  inside expressions.
- `postgres/src/backend/optimizer/plan/subselect.c` —
  `SS_make_initplan_from_plan` (uncorrelated subqueries
  become initplans, evaluated once).
- `postgres/src/backend/executor/nodeSubplan.c` —
  `ExecScanSubPlan` runtime drain.

## Decisions

### Scope: scalar uncorrelated only

A scalar subquery returns at most one row × one column. v0
recognises the syntax `( SELECT … )` in expression position;
it does **not** yet support:

- `IN (subquery)` / `NOT IN`.
- `EXISTS (subquery)`.
- Correlated subqueries (parameter pull-up). The inner plan
  in v0 cannot reference outer-row columns.

(`ANY` / `SOME` / `ALL` were also unsupported at the time this section was
written; see the "Follow-up: ANY / SOME / ALL" section below — closed
2026-07-05.)

Trying any of those raises a 0A000 feature-not-supported error
either at parse time (for `EXISTS`/`IN` whose grammar isn't
recognised) or at planning time (for a subquery whose inner
references undefined columns).

### Parser: paren-then-SELECT triggers SubqueryExpr

`parsePrimary` already handles `(expr)` for grouping. The
loop's change peeks one token after the `(`: if it's the
`SELECT` keyword, the parser delegates to `parseSelect()` and
wraps the result in a `parser.SubqueryExpr{Inner *SelectStmt}`.
Otherwise it falls through to the existing
parenthesised-expression path.

This avoids ambiguity because every TPC-H subquery is at the
beginning of the parenthesised group; row-constructor `(a, b)`
and parenthesised-expression `(x + 1)` never start with the
SELECT keyword.

### Planner: SubqueryExpr wraps a planned inner Node

The planner's expression rewriter (`resolveExpr` and the
after-aggregate variant) gets a new case that calls
`planSubqueryExpr(x, ctx.cat)`, which invokes `Plan(inner,
cat)` recursively and stuffs the result into a
`planner.SubqueryExpr{Plan Node}`.

Threading the catalog through `resolveContext.cat` (set once
at the top of `planSelect`) avoids passing it as a separate
arg to every helper. `agg.input.cat` carries the same value
into the after-aggregate site.

The analyzer types the expression as `unknown`, which lets
`isAssignable`'s unknown-coercion rule keep comparison and
assignment sites happy. Real type unification waits on the
type system.

### Executor: build-once-per-evaluation, drain to ensure cardinality

`evalSubquery(x, ctx)` follows the obvious shape:

1. `Build(x.Plan)` produces an Operator.
2. `op.Open(ctx)` and the first `Next()` give the result row.
3. EOF before the first row → `NullDatum` (matches upstream
   "scalar subquery returned no rows is NULL").
4. The row must have exactly one column — multi-column
   triggers SQLSTATE 42601.
5. A second `Next()` checks no extra rows exist —
   multi-row triggers SQLSTATE 21000
   (`cardinality_violation`), upstream-aligned message
   "more than one row returned by a subquery used as an
   expression".
6. `op.Close()` always runs (deferred).

Each evaluation rebuilds the operator; v0 doesn't yet cache
the result across evaluations of the same `SubqueryExpr` in
the same query. For uncorrelated scalar subqueries this means
the cost is paid per outer-row lookup, which is correct but
suboptimal — the upstream "initplan" pattern (evaluate once,
reuse value) is a follow-up optimisation, not a correctness
issue.

## Verification

End-to-end against `goopg start -D <dir>` with upstream
psql 18.3:

```
CREATE TABLE t (id INT, val INT);
INSERT INTO t VALUES (1,10),(2,20),(3,30),(4,40);

-- Q15-shape: max() in scalar subquery
SELECT id, val FROM t WHERE val = (SELECT max(val) FROM t);
-- (4, 40)

-- > comparator
SELECT * FROM t WHERE val > (SELECT min(val) FROM t);
-- (2,20),(3,30),(4,40)

-- Empty result → NULL → no match
SELECT id FROM t WHERE val = (SELECT val FROM t WHERE id = 999);
-- 0 rows

-- Cardinality violation
SELECT id FROM t WHERE val = (SELECT val FROM t);
-- ERROR: 21000 cardinality_violation
```

`TestParseSubqueryExpr` pins the AST shape.

### IN / NOT IN / EXISTS / NOT EXISTS (uncorrelated)

Adds three more AST shapes alongside SubqueryExpr, all
following the same uncorrelated-only contract:

- `parser.InExpr{Operand, Negated, Subquery, List}` — either
  Subquery or List is non-nil. The parser detects the right
  side: a leading `SELECT` keyword inside the parens is
  treated as a subquery; otherwise a comma-separated value
  list. The grammar hooks in at comparison precedence inside
  `parseExprPrec` so `expr IN (...)` and `expr NOT IN (...)`
  parse without back-tracking. NOT IN is a two-keyword
  lookahead (`KwNot` followed by `KwIn`).
- `parser.ExistsExpr{Negated, Subquery}` — leading EXISTS
  keyword in primary expression position. NOT EXISTS parses
  as `UnaryOp{NOT, ExistsExpr{Negated:false}}`; the executor's
  bool-NOT inverts the result. (Setting Negated at parse
  time would also work; the UnaryOp shape is simpler and
  reuses existing NOT semantics.)

Planner (`planInExpr` / `planExistsExpr`):
- IN with a subquery plans the inner SELECT recursively
  through `Plan(s.Subquery, ctx.cat)` and stores the result
  on `Plan`.
- IN with a value list resolves each list element through
  `resolveExpr` so column refs in the list resolve
  correctly.
- EXISTS unconditionally plans the inner SELECT.

Executor:
- `evalInExpr` materialises the inner set per evaluation
  (no caching across rows). Three-valued logic:
  - Operand NULL → NULL.
  - Inner contains a NULL and outer doesn't match a non-NULL
    value → NULL.
  - Inner empty → false (NOT IN: true).
  - Match found → true (NOT IN: false).
  Multi-column subqueries surface 42601.
- `evalExistsExpr` opens the inner plan, asks for one row,
  returns `hasRow != Negated`. Output columns are ignored —
  `SELECT 1` is the canonical body but anything else works.

End-to-end verified via psql 18.3:
- `WHERE p_partkey IN (SELECT ps_partkey FROM partsupp)` →
  matching rows.
- `WHERE p_partkey NOT IN (...)` → non-matching rows.
- `WHERE p_partkey IN (1, 4)` → value-list form.
- `WHERE EXISTS (SELECT 1 FROM partsupp WHERE …)` → all
  rows when inner non-empty, none when empty.
- `WHERE NOT EXISTS (...)` → inverted.

### Correlated subqueries (parameter pull-up)

TPC-H Q4 / Q21 / Q22 use the shape `EXISTS (SELECT 1 FROM
inner WHERE inner.col = outer.col …)`. Without parameter
pull-up the inner SELECT can't reference outer-scope
columns. v0 implements it via a lexical-scope chain on both
the analyzer and planner sides, plus an outer-row stack on
the executor's Context.

- `analyzer.scope.parent`: each scope carries its lexical
  parent. `resolveColumnRefType` walks the chain: local first,
  then up. Found at parent level returns the column's type
  (no special node — analyzer just type-checks).
- `analyzer.OuterScope` (exported) wraps the internal scope
  for planner-side construction. `analyzer.SetOuterScope`
  stashes the chain in a package-level channel for the
  recursive `Analyze()` call to pick up. Mirrors planner.planParent.
- `planner.resolveContext.parent`: same pattern. New
  `resolveColumnRefAt` checks each level; on a parent-level
  hit, emits a `planner.OuterColumnRef{Level, Index}`
  (1-based level — matches upstream's `Var.varlevelsup`).
- `planner.planSelectWithParent`: the entry point for
  subquery planning. Sets `planParent` (so planSelect's ctx
  inherits via `ctx.parent`) AND constructs the analyzer's
  `OuterScope` chain via `buildAnalyzerOuterScope`, calls
  `analyzer.SetOuterScope`, then `Plan()`. Both restorers
  are deferred.
- `executor.Context.OuterRows`: a `[]Row` stack. Push /
  pop per evaluation in `evalSubquery` / `evalExistsExpr`
  / `collectInValues`. `evalOuterColumnRef` reads
  `OuterRows[len(OuterRows)-Level]` — Level 1 is the
  innermost outer scope.

End-to-end verified via psql 18.3:
- TPC-H Q4 shape `EXISTS (SELECT 1 FROM lineitem WHERE
  l_orderkey = o.o_orderkey AND l_status = 'F')` correctly
  filters by per-row sub-results.
- NOT EXISTS variant flips the predicate.
- Correlated scalar subquery `(SELECT count(*) FROM
  lineitem WHERE l_orderkey = o.o_orderkey)` returns the
  right per-row count.

## Out of scope (deferred to subsequent loops)

- Initplan caching: evaluate uncorrelated subqueries once
  per query plan and reuse the result Datum. Performance
  win, no correctness change. Applies equally to
  SubqueryExpr / InExpr / ExistsExpr.
- Subquery decorrelation: rewriting correlated subqueries
  as semi-joins for asymptotic-better execution. v0's
  per-outer-row re-evaluation is correct but quadratic for
  large outers. TPC-H queries at SF1 will be slow but
  produce the right answer.
- `LATERAL` joins (related grammar; not used by TPC-H).

## Follow-up: ANY / SOME / ALL quantified comparisons (2026-07-05, M0122-0004)

The "out of scope" ANY/SOME/ALL gap above is closed. Previously only
`=`/`!=`/`<>` plus the four POSIX regex operators supported `ANY` against
an array literal or single scalar expression, and `ALL` was wired for `=`
only (via a `NOT (expr != ANY (...))` desugar). `SOME` was not a
recognised keyword at all, and no operator supported a `(SELECT ...)`
operand — only `IN (subquery)` did.

### Generalizing InExpr instead of adding a new node

`expr op ANY|SOME|ALL (...)` is a PostgreSQL `ScalarArrayOpExpr`: apply
`op` between the left operand and every element of the right-hand set,
then OR (ANY/SOME) or AND (ALL) the per-element results. goopg already had
half of this machinery — `InExpr.AnyOp` (element-wise operator) — from the
regex-ANY and `!=`-ANY work (M0097-0067/0068). This loop:

- Adds `InExpr.AllOp bool` (parser and planner) alongside the existing
  `AnyOp`. When `AnyOp != 0`, `AllOp` selects AND-of-comparisons instead of
  the default OR.
- Adds the `SOME` keyword (`internal/parser/token.go`/`keywords.go`),
  unreserved like the existing `ANY`, and threads it through every ANY
  check via a shared `isAnyOrSomeTok` helper.
- Extends `parseAnyTail` (`internal/parser/select.go`) to also accept a
  `(SELECT ...)` operand, mirroring `parseInTail`'s subquery detection —
  previously it only parsed an `ARRAY[...]` literal or a bare scalar
  expression.
- Adds one new dispatch block in `parseExprPrec` covering the operator ×
  quantifier combinations the pre-existing `=`/`!=`/`<>`/regex blocks
  didn't reach: `<`, `>`, `<=`, `>=` with ANY/SOME/ALL, and `!=`/`<>` with
  ALL. The pre-existing blocks are extended in place to accept `SOME` and
  (for the regex operators) `ALL`, rather than rewritten, to avoid
  disturbing their already-shipped behavior.
- `internal/executor/expr.go`'s `evalInExpr` gains an ALL branch alongside
  the existing ANY branch: AND of per-element comparisons, short-circuits
  false as soon as one element fails. NULL elements are skipped in both
  branches (pre-existing ANY simplification, kept consistent rather than
  fixed only for the new ALL path — see Known limitations below).
- The subquery form required zero new executor plumbing:
  `collectInValues` already drains an arbitrary single-column subquery
  plan for `IN (subquery)`; `AnyOp`/`AllOp` are read generically
  regardless of whether the source was `List` or `Plan`.

### Known limitations (not fixed by this loop)

- **NULL handling is not fully three-valued.** Upstream's ScalarArrayOpExpr
  returns NULL (not false) for `x = ANY(array)` when no element matches but
  at least one element is NULL, and symmetrically for ALL. goopg's ANY/ALL
  branches skip NULL elements and return a definite true/false. This
  simplification predates this loop (M0097-0068's ANY branch already did
  it); the new ALL branch intentionally matches it for consistency rather
  than being "more correct" than its sibling in an asymmetric way.
- Decorrelation/caching for the new subquery form rides the same
  non-correlated subquery cache as `IN (subquery)` — no new optimization
  work, no new correctness gap either.

## Cross-references

- TPC-H Q15 / Q17 / Q22 query bodies: HammerDB upstream
  `tpc-h/queries-93-orig.sql`.
- Parser AST extensions:
  [root-0010-parser.md](root-0010-parser.md).
- Planner architecture:
  [root-0011-planner.md](root-0011-planner.md).
- Executor architecture:
  [root-0012-executor.md](root-0012-executor.md).
