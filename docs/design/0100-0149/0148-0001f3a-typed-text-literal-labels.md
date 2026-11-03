# M0148\-0001f3a — a string literal passed to a text parameter prints as `'…'::text`

**Parent:** M0148\-0001f3 (item 3, typed literals) · **Status:** landed
2026\-11\-04; closed. Closes the aggregates.sql suite (M0148\-0001).

## Divergence

aggregates.sql, the last hunk of the suite:

```
PG 18.3                                       goopg (before)
Output: string_agg(x, ','::text)              Output: string_agg(x, ',')
```

TPC\-H Q15's filter shows the same thing: PG prints
`to_date('1996-01-01'::text, 'YYYY-MM-DD'::text)`, and goopg printed the
literals bare.

## PostgreSQL

A string literal is an `unknown` Const. Function resolution in
parse\_func.c (`func_get_detail` → `func_select_candidate`) picks the
overload and then coerces the Const to the declared parameter type. For a
`text` parameter, the Const is relabelled as text. ruleutils's
`get_const_expr` prints a Const of any type other than the few implicit
ones (int4, numeric, bool, unknown) with an explicit `::type`, which gives
`','::text`.

## Change

- **`catalog.BuiltinProcSignatures(name)`**
  (internal/catalog/builtin\_proc\_signatures.go). For every PG 18 built\-in
  `pg_proc` row with that name, it returns the argument typnames. The rows
  come from the generated `pgProcNamesByOID` / `pgProcArgTypeNamesByOID`
  tables.
- **`literalArgIsText`** (operators\_explain.go). For a `StringConst` at
  argument i of a `FuncCall`:
  - keep the overloads with the call's arity whose parameters accept the
    other arguments' known static types (`procParamAccepts`). Polymorphic
    `any*` and `record` parameters accept anything. An argument of unknown
    static type, or another literal, constrains nothing.
  - The literal gets `::text` only when at least one candidate survives and
    every survivor declares `text` at i.
  - `string_agg(bytea, 'x')` therefore stays unlabelled, since its
    surviving overload takes bytea.
- **Scope.** Only text parameters are handled, and only the label is
  affected. Evaluation is unchanged, because the literal was already coerced
  at execution.

## Verification

- `TestExplainTextLiteralArgLabel` pins PG 18.3's `string_agg(t, ','::text)`
  and `regexp_split_to_array(t, ','::text)`, and checks that the bytea
  `string_agg` literal is left unlabelled.
- **Regress runner.** `aggregates` passes (100% parity), stable over 3
  runs. A 32\-suite A/B (HEAD vs change) shows no other suite changed.
- **Testport.** `TestPort_RegressSuite/aggregates` passes in the full
  ordered run. The inventory row is flipped to `pass,yes`, and the
  regenerated coverage docs pass `make check-testport-inventory`.
  `TestPort_PgDump*` and `TestPort_Isolation*` also pass.
- **TPC gates.**
  - tpch\-spotcheck, the sf025 sweep (99/99) and acceptance (values
    identical) all pass.
  - The fire\-set fires Q15 on TPC\-H. Its plan text moved to PG's
    `to_date('…'::text, '…'::text)`; the query passes its execution check.
    The `'3 month'` vs PG's `'3 mons'` interval label is a separate,
    pre\-existing difference.

## Not covered

- **Literals coerced to non\-text parameters**, such as `'…'::date` or
  `'…'::interval` arguments. Their label also needs the type's canonical
  output text (PG prints `'3 mons'::interval`, not the input spelling).
- **Operator operands** (`col = 'x'::text`). They go through operator
  resolution, not function resolution.
- **User\-defined functions.** Only built\-in signatures are consulted.
