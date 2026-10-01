# M0146-0005 (part 6): slices 87+ — EXPLAIN constant and array rendering

Continuation of [m0146-0005-join-order-burndown-5.md](m0146-0005-join-order-burndown-5.md)
(slices 71-86), split per the design-doc size rule (D3). Same task and census
family.

## Slice 87: M0146-0005ch — an IN list prints as PG's folded array Const

PG's transformAExprIn turns `a IN (1, 2)` into a ScalarArrayOpExpr over an
ArrayExpr. eval_const_expressions folds the ArrayExpr to one array Const,
so EXPLAIN prints `(a = ANY ('{1,2}'::integer[]))`. goopg printed the
element list, `(a = ANY (1, 2))`, in every IN-list qual. None of the 51
array Consts PG prints over TPC-DS (each scale) matched.

- `inListArrayConst` returns the Const text for an all-literal list. The
  element type is select_common_type's pick for the column and the
  literals:
  - int2/int4 → `integer[]`, int8 → `bigint[]`;
  - an integer column against a decimal literal → `numeric[]` with
    `(a)::numeric`;
  - text → `text[]`, char(n) → `bpchar[]`, varchar → `text[]` with
    `(f)::text`, and canonical ISO dates → `date[]`.

  Elements are quoted as array_out does: double quotes around empty, NULL,
  or any element holding `"`, a backslash, braces, a comma or whitespace.
  Embedded `"` and backslashes are escaped. Any other list (a NULL, a
  non-literal, an unmodelled type) keeps the old rendering.
- NOT IN prints `(a <> ALL (…))`, and `<> ANY` keeps its own operator.
  The UnaryOp arm pushes an explicit `NOT (b IN …)` into the list as
  negate_clause does.
- The Index Cond SAOP renderer (`formatIndexCond`) is the sibling. It uses
  the same helper with the index column's catalog type. Its
  bounds-on-the-second-column form now parenthesises each clause:
  `((a = ANY (…)) AND (b > 1))`.

Test: `TestInListPrintsFoldedArrayConst` (13 PG 18.3 oracle lines; all 13
fail with the branch disabled). `TestSAOPExplainRendersAnyCond` now
expects PG's `'{2,4}'::integer[]`.

Movement:

- TPC-DS: goopg prints 49 of PG's 51 array Consts per scale (was 0). The
  rest are the
  `substr(ca_zip, …)` lists of Q8/Q15/Q45. There PG also casts the
  argument (`(ca_zip)::text`, the ledgered argument-cast gap) and
  ExprResultType does not resolve `substr(bpchar, …)`. The classifier treats
  qual text as rendering inside MATCH, so CATEGORIES-EXCL-MATCH is
  unchanged (rendering 12 / 15).
- Regress, same-order A/B over 12 cases: btree_index 507 → 500,
  create_index 3310 → 3309. Five array lines now match PG exactly (was 0).
  Most other changed lines stay mismatched because of the plan shape or
  partition-child qualification around them.
- Gates: units, spotcheck, sweep 96/96, arm 24/24, fire set and ea-ratchet
  (10) pass.

Left (ledgered): non-literal lists (`ARRAY[...]` and PG's
Var/non-Var split into OR arms), lists with NULL elements, float, timestamp
and other element types, and a function-call operand whose type
ExprResultType cannot resolve.

Evidence: `analysis/m0146/m0146-0005/slice87/`.

## Slice 88: M0146-0005ci — a literal prints as the Const it was coerced to

PG coerces a literal to the other operand's type during parse analysis
(make_op → coerce_type), so EXPLAIN prints the resulting Const through
get_const_expr: `(ca_gmt_offset = '-6'::numeric)`,
`(s_state = 'TN'::bpchar)`, `(revenue / '50'::numeric)`. goopg printed the
literal as written (`= -6`, `= 'TN'`).

- `formatCoercedLiteralOperands` covers `= <> < <= > >= + - * /` with one
  literal side and one side whose type ExprResultType resolves.
  `coerceLiteralText` applies the operator's input type:
  - int2/int4/int8 operand: the literal stays int4 (cross-type operators);
  - numeric: an integer becomes `'N'::numeric`, a decimal prints bare
    unless negative;
  - an integer operand against a decimal is cast, `((a)::numeric > 2.5)`;
  - char(n) → `'x'::bpchar`, text → `'x'::text`, varchar → `((f)::text =
    'x'::text)`.
- `intConstText` is get_const_expr's int4 arm everywhere: a negative
  literal is `'-6'::integer`, and one past int4's range is
  `'N'::bigint`.
- Typed literals print format_type's names (`timestamp without time zone`),
  and a timestamp's ISO value goes through timestamp_out's form
  (`'2001-07-15 00:00:00'`).

Test: `TestLiteralPrintsAsCoercedConst` (13 PG 18.3 oracle lines; 8 fail
with the coercion disabled, the rest pin the int4 and timestamp arms).
Two older tests that pinned goopg's unlabelled text now expect PG's
`'3'::text` / `'a'::text`.

Movement (TPC-DS SF0.25; SF1 identical):

| label | PG | goopg before | goopg now |
|---|---|---|---|
| `::numeric` | 152 | 0 | 141 |
| `::bpchar` | 207 | 49 | 207 |
| `::text` | 68 | 6 | 33 |
| `::integer` | 28 | 19 | 28 |
| `::timestamp without time zone` | 28 | 0 | 28 |

No label goopg prints is missing from PG's plan for the same query
(per-query multiset check). CATEGORIES-EXCL-MATCH is unchanged (rendering
12 / 15). In regress the moves are small; literal lines in partition_prune
now match PG's `'a'::bpchar`, but their column qualification still
differs. Gates: units, spotcheck, sweep 96/96, arm 24/24, fire set and
ea-ratchet (10) pass.

Left (ledgered): string literals that are target or union-arm output
columns (`'store channel'::text`), LIKE patterns (`~~ 'Unknown%'::text`),
literals inside CASE / COALESCE results (Q39, Q78 `'0'::bigint`), function
arguments, and float or date-arithmetic operands.

Side finding (filed S2 as M0146-0040, not worked): `date + integer`
returns a timestamp-formatted value of type unknown, and errors with a
literal date operand.

Evidence: `analysis/m0146/m0146-0005/slice88/`.

## Slice 89: M0146-0005cj — a Sort key over a WindowAgg prints its window function

PG computes `sum(x) * 100 / sum(sum(x)) OVER (…)` in the WindowAgg's
targetlist, and the Sort above keys on that entry. EXPLAIN therefore prints
`((((sum(x)) * '100'::numeric) / sum((sum(x))) OVER w1))`:

- the window function is evaluated in place, so it prints bare with its
  `OVER w1`;
- each input column is an OUTER_VAR into the window's child, parenthesised
  around its non-Var referent (get_special_variable), so the aggregate
  prints as `(sum(x))`, also inside the window function's argument;
- the whole key is a non-Var referent and takes the outer pair.

goopg printed the output labels, `(((sum * 100) / sum))`, and
`Sort Key: rank` for a bare window result.

- `windowKeyText` rewrites the key's column references for display with
  `optimizer.CloneExprMapColumnRefs` (new; a thin exported wrapper over
  `cloneExprRefs`):
  - a window result becomes `windowFuncText` (`name(args) OVER wname`,
    with arguments chased through the window's child);
  - an input column is chased through `resolveKeySource` and wrapped
    unless it lands on a plain column.

  Stand-ins print through the `boundaryKeyName` map and keep the column's
  type, so slice 88's literal coercion still labels `'100'::numeric`.
- `windowUnderNarrowing` also finds the WindowAgg under a column-selecting
  Project, which goopg places where PG's WindowAgg emits the final
  targetlist (`ORDER BY rk` → `(rank() OVER w1)`).

Test: `TestSortKeyOverWindowAggDeparsesWindowFunc` (3 PG 18.3 oracle
lines; all fail with the arm disabled).

Movement:

- TPC-DS Q12/Q20/Q98 go from `MATCH [rendering]` to `MATCH []` at both
  scales. Rendering including MATCH goes 18 → 15 (SF0.25) and 19 → 16
  (SF1), and fully identical plans 32 → 35 and 24 → 27.
  CATEGORIES-EXCL-MATCH is unchanged (12 / 15).
- What is left in those three plans is a missing `Parallel Hash` line under
  a parallel hash join, which the classifier does not score.
- Regress groupingsets 1893 → 1880: the cube-over-window EXPLAIN hunk now
  matches PG completely.
- Gates: units, spotcheck, sweep 96/96, arm 24/24, fire set and ea-ratchet
  (10) pass.

Left (ledgered): window functions with FILTER, keys over a WindowAgg
reached through other nodes (Filter, Subquery Scan), Group Key / Window
lines that read window results, and the missing `Parallel Hash` node line.

Evidence: `analysis/m0146/m0146-0005/slice89/`.
