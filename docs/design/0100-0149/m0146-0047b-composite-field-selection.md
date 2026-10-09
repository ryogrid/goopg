# M0146-0047b — composite field selection `(expr).field`

Status: done 2026-10-06 (`709a5e956`).

## Symptom

`SELECT (b).x FROM wb b`, `SELECT (ROW(1,2)).f1` and
`SELECT (c).p FROM (SELECT ROW(1,2)::ct AS c) s` failed with `syntax error
at or near …`. PG 18.3 reads the field. Its grammar is c_expr
`'(' a_expr ')' opt_indirection`; transformIndirection then hands each
`'.' attr_name` step to ParseFuncOrColumn, which builds a FieldSelect.

## Grammar (`grammar/pg_grammar.y`)

- `field_select_expr` is reached from c_expr, beside the existing
  `(expr).*` rule:
  - `'(' a_expr ')' '.' attr_name`
  - `field_select_expr '.' attr_name`, left-recursive, so `(q).c1.i` nests.
- The S/R conflict count stays at the pinned 60.
- The new parser node is `FieldSelect{Arg, Field}`. Its position is the
  `(`.
- `(a, b).f1` stays a syntax error, as in PG (implicit_row takes no
  indirection).
- Golden review: three former `assertBothReject` forms became
  `assertParity` because PG accepts them, plus seven new pins.

## Planner (`internal/optimizer/fieldselect.go`)

`resolveFieldSelect` resolves the operand with the resolver's usual
precedence: a bare name is a column first, then a relation's whole-row
reference (transformColumnRef). Then it handles each case:

| operand | result |
|---|---|
| a relation's whole-row reference `(b)` / `(b.*)` | the column itself — PG reduces FieldSelect over a whole-row Var to the Var |
| an anonymous row `(ROW(1,2))`, `((1,2))` | element N for field `fN` |
| a row constructor cast to a composite type | the element, cast to the field's type |
| any other composite value (column, function result, nested field) | `FuncCall __goopg_field_select(arg, idx, 'field')` with ReturnType = the field type |

- **Why a FuncCall.** A new optimizer node would need an arm in every
  expression walker (column remapping, narrowing, shifting), and a missed
  arm silently reads the wrong column. Every walker already recurses into
  FuncCall arguments.
- **Field types** come from `LookupCompositeTypeFields`, or from the
  table's columns for a table row type.
- **Errors** mirror PG's, with the caret on the operand:
  - `column "f" not found in data type t` (42703);
  - `could not identify column "f" in record data type` (42703);
  - `column notation .f applied to type T, which is not a composite type`
    (42809).
- **Column naming:** `targetMeta` names an unaliased target after the
  field (FigureColname's A_Indirection rule).
- **Grouped queries:** `resolveFieldSelectAfterAggregate` maps `(b).y` onto
  the qualified column `b.y`, so it matches its GROUP BY slot. The funcdep
  prefetch walk visits the same qualified column.
- **Registration:** `FieldSelect` is registered in `allExprTypes`; its
  structural key covers `Arg` and `Field`.
- **Walkers:** the analyzer types it `unknown` after walking the operand.
  The window, SRF, window-ref and bare-aggregate walkers recurse into it.

## Executor

- `evalFieldSelect` evaluates the operand:
  - a NULL composite or a NULL field gives NULL;
  - otherwise it splits the record text with `parseRecordText`, a port of
    record_in's tokenizer (empty unquoted field = NULL, quotes group,
    doubled quote, backslash escape);
  - the field is cast to the call's ReturnType.
- EXPLAIN renders the call as `(arg).field`, following ruleutils'
  T_FieldSelect: parentheses unless the operand is another field
  selection, and a quoted field name.

## Fixed on the way

`resolveExprAfterAggregate` type-asserted a `ColumnRef` and panicked the
backend on a bare whole-row reference in a grouped query (`SELECT b,
count(*) FROM t b GROUP BY x`; already crashing at HEAD). It now raises
PG's 42803 `column "b.*" must appear in the GROUP BY clause …`.

## Verification

- `TestFieldSelection`, every want PG 18.3's output:
  - 22 value cases: whole-row, anonymous row, composite column, function
    result, NULL composite and NULL field, quoted text, chained
    selection;
  - output column names;
  - 5 error cases, including the grouped whole-row error.
- `TestParseRecordText` covers record_in's rules.
- Server probes against PG on :5534 are identical, including regress
  rowtypes' quadtable example.
- Gates:
  - units PASS;
  - tpch-spotcheck PASS;
  - acceptance arm 24/24;
  - sf025 96/96, plan shapes 99/99 same;
  - fire set: no fires;
  - ea-ratchet PASS.
- Regress A/B over 12 files:
  - rowtypes 1384 → 1326 diff lines, arrays −6, plpgsql −8;
  - join +13: `(t2.*).unique1` now plans, replacing a syntax error;
  - create_view and domain: syntax errors became other errors on
    statements that already failed;
  - no panics.

## Not covered

- **Records whose structure PG recovers through `expandRecordVariable`**
  still fail with PG-style errors, where PG answers:
  - a subquery or CTE column holding an anonymous `row(...)` (`(r).f1
    from (select row(1, 2.0) as r) ss`);
  - a whole-row of a VALUES/derived table (`(r).column2`);
  - a function with OUT parameters (`(ss.a).x` over
    `_pg_expandarray`).
- **Assignment indirection** (`INSERT INTO t (q.c1.r)`, `UPDATE t SET
  q.c1.r = …`, FieldStore) and `$1.field` inside SQL function bodies are
  still syntax errors.
- **Subscripts after a field** (`(x).f[1]`) are not part of
  `field_select_expr`.
- **Optimizations:** the parser walkers that decide index-only scans,
  outer-join strictness, HAVING→WHERE motion and special-join relids
  decline a FieldSelect conservatively. A query using one can miss those
  optimizations; it does not get a wrong answer.
- **DROP TYPE dependencies** (found while probing): goopg drops a
  composite type that a table column still uses, and the column falls
  back to text. PG refuses with 2BP01.
