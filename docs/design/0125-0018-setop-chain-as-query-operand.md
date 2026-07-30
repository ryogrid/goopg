# M0125-0018 — a parenthesised set-op chain is a *query* operand, not an expression

status: accepted
date: 2026-07-29
area: parser (`internal/parser/select.go`)
series: M0125 / TPC-DS round-2 fixes — fourth member of the
[0125-0006](0125-0006-setop-chain-associativity.md) discovery class
(see also [0125-0016](0125-0016-setop-operator-precedence.md),
[0125-0017](0125-0017-setop-head-branch-sort-limit.md))

## 1. Symptom

Two of the four gaps M0125-0006's differential surfaced were parse failures:

```
select … where x in ((select …) except (select …) except (select …));
  ERROR:  syntax error: expected ')' to close IN list (got except)

select … where exists ((select …) except (select …));
  ERROR:  syntax error: EXISTS requires a parenthesised SELECT (got ()
```

PostgreSQL 18.3 accepts both. The same probe found a **third** sibling that the
original report did not name — the quantified form:

```
select … where x = any ((select …) union (select …));
  ERROR:  syntax error: expected ')' (got union)
```

and, more importantly, a **quiet** variant of the same root cause that produced
no syntax error at all:

```
-- PG 18.3: t          goopg (pre-fix): ERROR 21000
select 1 in ((select 1 union select 2));
```

goopg read `((SELECT …))` as a one-element *value list* holding a scalar
subquery, so a multi-row inner query raised `21000 more than one row returned
by a subquery used as an expression` where PostgreSQL simply answers the `IN`.
That is the same blind-spot class as 0125-0006/-0017 wearing a different coat:
the loud half is a syntax error a user notices immediately, the quiet half is a
runtime error (or, for a single-row inner query, a *silently different
meaning*) on a query PG runs fine.

## 2. What PostgreSQL actually spells

`postgres/src/backend/parser/gram.y` makes a parenthesised query a first-class
operand of all three constructs:

```
a_expr IN_P in_expr
in_expr:  select_with_parens | '(' expr_list ')'

EXISTS select_with_parens

a_expr subquery_Op sub_type select_with_parens
sub_type: ANY | SOME | ALL
a_expr subquery_Op sub_type '(' a_expr ')'

select_with_parens: '(' select_no_parens ')'
                  | '(' select_with_parens ')'
```

Three things follow, and all three were violated:

1. `select_with_parens` **nests**, so the operand may begin with any number of
   `(`. A parenthesised set-op chain always begins with two.
2. `select_no_parens` carries `opt_sort_clause` / `opt_select_limit` / a
   locking clause, so `EXISTS ((A) EXCEPT (B) ORDER BY 1 LIMIT 1)` is legal.
3. `EXISTS` has **no** expression alternative at all, while `IN` and
   `ANY/SOME/ALL` each have one — which is exactly what makes the choice hard.

## 3. Root cause

`parseInTail`, `parseExistsExpr` and `parseAnyTail` each decided "query or
expression?" by asking a single question after consuming the `(`:

```go
if p.cur().Kind == TokenKeyword && (p.cur().Keyword == KwSelect || p.cur().Keyword == KwValues) {
```

One token of lookahead. A nested `(` fails that test, so:

* `parseInTail` fell through to its value-list arm, which parsed `(A)` as a
  scalar `SubqueryExpr` and then demanded `,` or `)` — hence
  `expected ')' to close IN list (got except)`;
* `parseAnyTail` did the same via its array-element arm;
* `parseExistsExpr`, having no fallback, reported the operand as malformed.

The scalar-subquery path in `parsePrimary` (select.go ~2900) had already learned
this lesson for its own context in M0097-0042: it peels the leading `(` run and
delegates to `parseParenthesisedSelectStmt`. The three operand parsers never
got the same treatment, which is why derived tables, CTE bodies and scalar
subqueries all accepted the chain (pinned by 0125-0006's tests) while these
three did not.

## 4. Why one token of lookahead is not enough — and neither is peeling parens

Peeling the `(` run and calling it a query is **wrong**, because both
alternatives can start with `((SELECT`:

| statement | PG 18.3 | production |
|---|---|---|
| `select 1 in ((select 1) union (select 2))` | `t` | `select_with_parens` |
| `select 1 in ((select 1 union select 2))` | `t` | `select_with_parens` |
| `select 1 in ((select 1),(select 2))` | `t` | `'(' expr_list ')'` |
| `select 1 in ((select 1)::int)` | `t` | `'(' expr_list ')'` |
| `select 2 in ((select 1) + 1)` | `t` | `'(' expr_list ')'` |

The discriminator is not what follows the `(` — it is **what follows the
group's matching `)`**. A `,`, a `::` or an arithmetic operator continues an
*expression*; a set operator, `ORDER`, `LIMIT`, `OFFSET`, `FOR`, or the `)`
that closes the operand outright continues a *query*.

The third row above is why the bare `((SELECT …))` form must resolve to
`select_with_parens` and not to a one-element expr_list: PG answers `t` to
`select 1 in ((select 1 union select 2))`, and the expr_list reading cannot —
as a scalar subquery it raises `21000`, which is precisely what goopg did.

## 5. The fix

One helper, three call sites.

```go
func (p *parser) selectWithParensAhead() bool
```

in `internal/parser/select.go`, applied at the top of `parseInTail`,
`parseExistsExpr` and `parseAnyTail` before each one's existing SELECT/VALUES
test. It returns true only when both conditions hold:

1. peeling the leading run of `(` reaches `SELECT` or `VALUES` — so `((1),(2))`
   and `(a + b)` stay expressions; and
2. `continuesParenthesisedQuery(tokenAfterMatchingParen)` — a `)` (the operand
   ends here), or one of `UNION` / `INTERSECT` / `EXCEPT` / `ORDER` / `LIMIT` /
   `OFFSET` / `FOR`.

Condition 2 walks the token stream with a depth counter; it never consumes,
so no backtracking machinery is needed (the parser has none — `p.idx` is the
only position state and nothing else would be rolled back).

When it fires, `parseQueryOperandWithParens` runs the existing
`parseParenthesisedSelectStmt` — which already handles arbitrary paren nesting,
trailing set operators, precedence (0125-0016) and the head branch's own
`ORDER BY`/`LIMIT` (0125-0017) — under the `SELECT … INTO is not allowed here`
error context all three sites already established, and the caller then consumes
the operand's own closing `)`.

Because the shared entry point is `parseParenthesisedSelectStmt`, the operand
inherits every set-op repair the rest of M0125 landed for free; that is
asserted, not assumed (`in_head_branch_limit_then_union_all` below).

`parseAnyTail` returns the same `*InExpr{Subquery: …}` node its existing
subquery arm returns, so its callers' `AnyOp` / `AllOp` post-assignment is
untouched.

## 6. Scope this does **not** widen

`EXISTS` keeps its strict operand: `selectWithParensAhead` declines
`EXISTS ((1))`, which falls through to the original
`EXISTS requires a parenthesised SELECT` error, matching gram.y's lack of an
expression alternative there. Pinned by
`TestExistsStillRejectsNonQueryParens`.

`parseAnyTail` still accepts a comma-separated element list
(`x = ANY ((select 1),(select 2))`), which PG rejects — `sub_type` admits a
single `a_expr`, not an `expr_list`. That laxness is pre-existing and untouched
here; see the deferral ledger row.

## 7. Acceptance — by value, against the oracle

Every expectation was captured from PostgreSQL 18.3 (the read-only oracle, port
65438) running the identical statement against an identically loaded fixture,
never derived from goopg. The fixture is shared with 0125-0006/-0017:
`a:{1,2,3} b:{2} c:{3} d:{1,3} e:{2,4} f:{4} g:{2,3} h:{9}`.

* `internal/executor/setop_query_operand_test.go` — 20 end-to-end cases:
  IN / NOT IN / EXISTS / NOT EXISTS / `= ANY` / `<> ALL` over a parenthesised
  chain, the bare `((SELECT …))` multi-row form, `((VALUES …))`, double
  wrapping, a chain with `ORDER BY … LIMIT` inside the operand parens, the
  0125-0017 composition, and five controls that must not move.
* `internal/parser/setop_query_operand_test.go` — 16 AST-shape pins for the
  cases where both readings agree numerically and only the parse tree
  distinguishes them, including the `::`/`+`/`,` expression controls.

**Proved to fail at pre-fix `74f4b264`**: 14 executor subtests and 10 parser
subtests failed there while every control passed, i.e. the tests discriminate
the fix rather than the fixture.

`NOT EXISTS ((SELECT x FROM b) EXCEPT (SELECT x FROM b))` is deliberately in the
matrix: it is the only case whose answer changes if the chain parses but is
never actually *evaluated*.

## 8. Reach

**TPC-DS cannot reach this defect.** A scan of all 99 SF0.5 query files (comment
stripped, case-folded, `\b(in|exists|any|all)\s*\(\s*\(` across newlines) found
zero occurrences, so the SF0.5 gate is structurally unable to observe the change
— the same conclusion 0125-0017 reached by reflection walk, and the reason this
item was ranked as parser hygiene rather than a round-2 blocker. It matters
because hand-written SQL and the regress corpus use the shape freely, and
because the quiet `21000` variant is a wrong-answer class, not a syntax class.

## 9. Deferred

See `.ralph/deferral_ledger.md` (2026-07-29): `ANY/SOME/ALL` still accepts an
`expr_list` where PG's `sub_type` admits a single `a_expr`, and
`selectWithParensAhead`'s continuation set omits `FETCH` (goopg has no `KwFetch`
token, so `FETCH FIRST … ROWS ONLY` is unreachable from any operand today).
