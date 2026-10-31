# 0122-0015f — `x = ANY (non-array)` is rejected with PG's 42809

**Milestone:** M0122-0015 (test-suite porting family; child 0015f, Kind: impl,
Parent: M0122-0015). Filed 2026-10-12 while testing M0122-0015b.

## The divergence

`SELECT 1 = ANY (1);` returned `t` in goopg: the quantified `= ANY` desugars
to the IN shape (parser `quantifiedAny`), and the parenthesised scalar
arrives as a one-element list, so the query evaluated as `1 IN (1)`. PG 18.3
rejects it at parse-analysis: `make_scalar_array_op` (`parse_oper.c:801`)
demands an **array-typed** right operand for the `AEXPR_OP_ANY/ALL` forms,
answering

```
ERROR:  42809: op ANY/ALL (array) requires array on right side
LINE 1: SELECT 1 = ANY (1);
                 ^
```

(measured byte-for-byte against the reference oracle, caret under the
comparison operator, SQLSTATE on the wire included). The same rule covers
`< ALL (1)`, `!= ANY (1)` and the `OPERATOR(pg_catalog.=) ANY (1)` spelling —
all measured rejecting.

## goopg's desugar map (why a naive check would break legal SQL)

`x op ANY (...)` reaches the planner as `parser.InExpr{Quantified: true}`:

| form | AST shape | PG | action |
|---|---|---|---|
| `op ANY (SELECT …)` | `Subquery != nil` | AEXPR_OP_ANY_SUBLINK, legal | untouched |
| `op ANY (ARRAY[1])` / `(ARRAY[1,2])` | direct constructor **spliced** into `List` (scalar items!) | legal (`array[1]` **is** an array) | exempt via the new `SplicedArray` marker |
| `op ANY (ARRAY[1]::int4[])` | spliced, cast discarded | legal | exempt (same marker) |
| `op ANY ((ARRAY[1]))` | one-element list holding `ArrayConstructorExpr` → resolves to an `array_construct` FuncCall | legal | `funcCallResultType` reports `IsArray` → accepted |
| `op ANY ('{1,2}'::int4[])` | CastExpr, target `[]` suffix | legal | `exprIsArrayTyped` → accepted |
| `op ANY (arr_col)` / `(f_returning_array())` | ColumnRef / FuncCall with array type | legal | `exprIsArrayTyped` → accepted |
| `op ANY (1)` / `(i_flag)` / `(i_flag+1)` / `(true)` | one-element scalar | **42809** | **rejected** (the fix) |
| `x IN (1)` | `Quantified == false` | AEXPR_IN, legal | untouched (`oneElementInAsComparison` arm) |

The splice marker exists because the AST alone cannot distinguish a spliced
`ARRAY[1]` (scalar element, legal) from a parenthesised `(1)` (scalar
expression, 42809) — the grammar's `'(' a_expr ')'` leaves no node, exactly
the reason `unwrapAnyArray` already consults the token stream
(`directArrayAt(listPos)`). `unwrapAnyArray` now returns `(list, spliced)`
and all three splice sites record it: `quantifiedAny`, the new
`quantifiedAll` ctor (the two `.y` ALL actions collapsed into it — the
grammar's action now calls into the retained Go files, per playbook §12.6),
and `parseAnyTail`'s direct-ARRAY branch.

## Enforcement

`checkAnyAllArrayOperand` (`internal/optimizer/planner.go`) runs on the
**resolved** list right after each builder fills it, at both twin producers:
`planInExpr` (the main path) and `resolveInListAfterAggregate` (the post-
GROUP BY arm, planner.go:10432) — Rule-#2 sibling discipline. It rejects
`Quantified && Subquery == nil && !SplicedArray && len(List)==1` when
`ExprResultType` resolves the item and `exprIsArrayTyped` says non-array.
Unresolvable items (NULL, exotic nodes) fall through to today's runtime
behaviour — over-rejection is the forbidden direction.

## Ledgered residues (see `.ralph/deferral_ledger.md` 2026-11-01 row)

* `op ANY ('{1,2}')` / `op ANY ('b')`: PG coerces the UNKNOWN literal to the
  operand's array type (array_in: `1 = ANY('{1,2}')` → t; `'a' = ANY('b')` →
  22P02 "malformed array literal"). goopg evaluates a scalar text comparison
  (`f` for the `'b'` case). **StringConst is explicitly exempt** so this
  status-quo path is not made worse; faithful coercion needs element-type
  inference + array_in + 22P02 — its own slice.
* `op ANY (a, b)`: goopg's grammar accepts a multi-element expr_list; PG's
  `a_expr_any` takes ONE `a_expr` (syntax error). Pre-existing laxness, also
  noted in 0125-0018's doc; len≥2 stays accepted (the splice form needs it).

## Pins & gates

`TestAnyAllScalarOperandRejected` (optimizer/saop_index_test.go): six
rejecting SQLSTATE+message cases, seven accepting forms incl.
`ANY (ARRAY[i_flag])` and `ANY ('{1}')`. `TestSAOPUnmovedShapes` swapped its
`column element` case (PG-rejected shape) for the legal `ARRAY[i_flag]`
form. Parser goldens: 40 InExpr dumps regenerated with the additive
`SplicedArray` field, 16 `true` (every direct `(array[...])` spelling
reviewed), 24 `false`; diff verified field-addition-only.
Gates: parser/optimizer/executor units; live psql parity (reject message +
caret + SQLSTATE byte-identical to the oracle; positive set unchanged);
tpch-spotcheck Q12=2/Q13=35; tpcds-fireset fires=none 99/99 (the 42809
shapes exist in no TPC query); sf025 sweep PASS; TPC-H acceptance arm 24
MATCH.
