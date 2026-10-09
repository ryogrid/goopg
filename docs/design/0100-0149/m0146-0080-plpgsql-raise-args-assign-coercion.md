# M0146-0080 — PL/pgSQL RAISE arguments and assignment coercion

Status: done 2026-10-07 (`f7f5b1f31`).

## Symptom

```sql
DO $$ BEGIN RAISE NOTICE 'b=% c=% d=%', (SELECT count(*) FROM r),
  (SELECT 5), 1 + (SELECT max(a) FROM r); END $$;      -- goopg: b= c= d=
DO $$ DECLARE v text; BEGIN v := (SELECT count(*) FROM r); END $$;
  -- goopg: variable "v" expects type "text" but got integer
```

PG 18.3 prints `b=2 c=5 d=3` and assigns `'2'`. The same two paths hid
more problems:

- `RAISE NOTICE 'x=%', 1/0` printed `x=` instead of raising 22012.
- `n int := 2.5` stayed `2.5`; PG rounds to 3.
- `b bool := 'true'` printed `true`; PG prints `t`.
- `d date := '2020-01-02'` printed a time of day.
- `a int[] := array[1,2]; a := a || 3` gave `{1,2}3`; PG gives `{1,2,3}`.

## Cause

- RAISE formatted its arguments through a separate evaluator that
  discarded every error and returned "" on failure. A sublink argument, a
  runtime error and an unsupported construct all printed as nothing.
- Assignments, DECLARE initialisers and RETURN used `coerceDatumToType`.
  That function is an exact-type check with a few hand-written arms. It is
  not PG's assignment coercion (exec_assign_value → exec_cast_value), which
  uses the assignment cast or, failing that, CoerceViaIO.
- The DECLARE type parser, `normalizeCatalogType` and
  `catalogTypeFromColumnType` dropped the `[]` suffix, so an array variable
  was typed as its element type.

Once RAISE propagated errors, the regress files exposed constructs the old
path had silently printed as empty:

- `pg_trigger_depth()`;
- `old.*` / `new.*` and `rec.field` on record variables;
- `INSERT … RETURNING … INTO`;
- `EXECUTE … INTO rec`;
- the `USING` clause being parsed as format arguments;
- `row(old.*) = row(new.*)`.

## PG

- `exec_stmt_raise` evaluates each parameter with `exec_eval_expr`, and its
  errors propagate.
- `exec_assign_value` casts to the variable's type with `exec_cast_value`:
  an assignment cast if one exists, otherwise I/O conversion through text.
  An unknown literal goes through the target type's input function.
- `numeric → int4` rounds half away from zero (numeric_int4).
- `ROW(rec.*)` expands the star into the record's fields
  (transformRowExpr). Row comparison follows `make_row_comparison_op`:
  - `=` is the AND of the element-wise `=` results;
  - `<>` is the OR of the element-wise `<>` results;
  - the ordering operators are a RowCompareExpr, which stops at the first
    unequal pair.

  So `ROW(1,NULL) = ROW(1,NULL)` is NULL, and `ROW(1,NULL) = ROW(2,NULL)`
  is FALSE.

## Change

- **RAISE.** `evalRaiseMsg` returns `(string, error)` and evaluates each
  argument with `evalPLpgSQLExpr`, the same binder SQL statements use.
  `cutRaiseUsing` removes the USING clause from the argument list.
- **`plpgsqlAssignCoerce`** is used by DECLARE in all four executors
  (function body, DO, CALL, nested block), by `:=` and by RETURN.
  - A string assigned to an integer, numeric, bool, date/timestamp or
    interval target goes through `evalCast`, the type's input function.
    time/timetz strings stay strings (the time-of-day display gap is
    ledgered).
  - Anything else goes through `coerceDatumToType`. On a 42804 mismatch it
    falls back to text I/O.
  - Input-function errors carry no position. PG reports them against the
    PL/pgSQL statement's own text, not the calling statement.
- **`coerceDatumToType`.** numeric → integer rounds half away from zero.
  Array targets pass through unchanged.
- **Array types.** The DECLARE parser records `[]`, and both catalog type
  helpers keep `IsArray`. An array subscript past `MaxArraySize`
  (134217727) raises 54000. Without this guard, regress `arrays` hung on
  `a[2147483647] := 42` once the assignment actually ran.
- **`pg_trigger_depth()`.** It reads `Context.TriggerDepth`, which
  `executePLpgSQLTriggerBody` increments. It is stamped int4 in the
  planner's builtin result types.
- **Record variables.** `rec.*` lowers to the record variable and
  `rec.field` to its `_rec_field` slot. Inside a ROW constructor (`ROW(…)`
  call or `(…)` row), `expandRecordStar` expands `rec.*` into the field
  slots. A field the event lacks becomes NULL.
- **INTO forms.** The parser accepts `INSERT/UPDATE/DELETE/MERGE …
  RETURNING … INTO` (`SelectIntoStmt.DML`). That form is implicitly STRICT:
  a second row raises P0003 with PG's hint. `EXECUTE … INTO rec` binds a
  record target.
- **Unknown names.** An unknown bare name in an expression reports
  `column "x" does not exist`, as PG's SQL parser does.
- **Row comparison.** `rowCtorElems` treats a `FuncCall{Name:"row"}` like
  a `RowExpr`, so `ROW()` comparisons take `evalRowToRowComparison` in both
  the interpreted and the compiled evaluator. Before, they compared the
  composite text, so `ROW(1,NULL) = ROW(1,NULL)` was TRUE. The comparison
  now implements the `=`/`<>` AND/OR semantics above. Previously a NULL
  pair before a decided pair returned NULL.

## Verification

- `TestPlpgsqlRaiseArgsAndAssignmentCoercion` covers:
  - RAISE sublinks;
  - DECLARE/assignment coercion and int[] concatenation;
  - pg_trigger_depth across nested triggers, with `old.*::text`;
  - `row(old.*) = row(new.*)` with NULL fields;
  - plain-SQL `ROW()` comparisons;
  - three error cases.

  Every want is PG 18.3's, and the test fails at HEAD.
- Server probes (`tmp/m80-probe.sql`, `tmp/m80-row.sql`) are identical to
  PG apart from error CONTEXT lines.
- Regress A/B over 9 files, fresh clusters:
  - plpgsql 4359 → 4188, triggers 2670 → 2570, arrays 3179 → 3171.
  - domain +6: a RETURN that errored now returns, but a numeric(5,2) typmod
    is not enforced on it (ledgered).
  - polymorphism: testpolym's lookup depends on state (filed M0146-0084).
  - The rest are identical.
- Gates: units, tpch-spotcheck, arm 24/24, sf025 96/96 (plan shapes
  99/99), fire set no fires, ea-ratchet PASS.

## Not covered (ledgered)

- PL/pgSQL error decoration: the `CONTEXT: PL/pgSQL function … line N at
  …` and `QUERY:` lines, and PL/pgSQL-relative `LINE` cursors.
- Nested `DECLARE … BEGIN … END` blocks inside a body are not parsed.
- time/timetz variables print with a 1970-01-01 date when their type is not
  carried.
- RAISE `USING` options (MESSAGE, DETAIL, HINT, ERRCODE) are cut but not
  applied.
- typmod and domain constraints are not enforced on assignment or RETURN.
- Zero-argument builtins other than pg_trigger_depth (pg_backend_pid,
  inet_client_port, txid_current, …) report text as their wire type.
- RETURN NEXT without an expression in a function with OUT parameters fails
  `missing expression`.
- TG_RELID is not a trigger variable.
