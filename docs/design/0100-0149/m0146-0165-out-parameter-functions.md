# M0146-0165 — the OUT-parameter function model

Status: landed 2026-10-13.
Parent: M0146-0084 (item 2a rank via the M0146-0055 pin).

## Problem

M0146-0084 made functions with OUT parameters callable. The rest of the model
was still missing. Against PostgreSQL 18.3 (`tmp/m0165/probe.sql`,
`probe2.sql`):

- **Omitted RETURNS.** `CREATE FUNCTION f(a int, OUT b int, OUT c text) AS …`
  (RETURNS omitted) was a syntax error. gram.y makes RETURNS optional.
- **PL/pgSQL OUT variables.** Output parameters were never declared in a
  non-set function, so `y := x * 10` raised `variable "y" does not exist`.
  Falling off the end raised "control reached end of function", and a bare
  `RETURN` raised "missing expression".
- **Several OUT parameters gave no record.**
  - A SQL function's multi-column final SELECT was rejected at CREATE ("must
    return exactly one column").
  - At run time only the first column was kept.
  - `(f(3)).c` raised "could not identify column".
- **RETURN NEXT without an expression** rendered non-string outputs through
  `StringValue()`. A RETURNS TABLE function's integer column came out NULL;
  this was already wrong on HEAD.
- **Named columns of a FROM-clause function did not resolve.** `select b
  from t1(2)` failed even for RETURNS TABLE functions (already on HEAD),
  although `SELECT *` worked.
  - The parse analyzer (`internal/parser/analyzer`) builds its own scope for a
    FROM-clause function.
  - For any function its dispatch did not know, it assumed one int8 column
    named after the alias.
- **A RETURNS TABLE function in the target list.** `select t1(2)` was not
  treated as set-returning, because its arity check counted the table columns
  as arguments.
- **pg\_proc.**
  - `pronargs` and `proargtypes` counted OUT parameters; PG counts inputs
    only.
  - The heap row left `proallargtypes` / `proargmodes` NULL.
  - `pg_get_function_arguments` printed `IN x integer` for a function; PG
    prints `IN` for procedures only.
- **Record text.** Record text escaped an embedded quote as `\"`, where
  `record_out` doubles it (`""`). The FROM-clause decomposer split on every
  comma, even inside quotes.

## Design

- **Grammar.** A second `create_function_stmt` alternative without
  `RETURNS fn_return`.
  - Its attribute list is the new non-empty `fn_attrs1`. With an empty list,
    an empty `fn_attrs` would have to be reduced before RETURNS, which also
    starts the first alternative.
  - `make gen-parser` still reports the known 60 shift/reduce conflicts.
  - The parser guard that pinned the rejection now pins acceptance, and the
    goldens gain the two parsed statements.
- **Result type** (`resolveOutParamReturnType`, interpret\_function\_parameter\_list's
  requiredResultType):
  - With output parameters (OUT, INOUT), the result is the single output's
    type, or record for several. An omitted RETURNS takes it.
  - A RETURNS that differs raises 42P13 "function result type must be X
    because of OUT parameters".
  - Without output parameters and without RETURNS, the error is "function
    result type must be specified".
  - RETURNS TABLE keeps its parsed `SETOF record`.
- **PL/pgSQL.**
  - `declarePLpgSQLOutParams` (shared by the set and non-set paths) declares
    the outputs. An INOUT parameter is already bound to its input.
  - `plpgsqlOutParamResult` returns the single output's value, or the record
    of all of them. It runs on a bare RETURN, on reaching the end of the body,
    and on RETURN NEXT without an expression.
  - RETURN with an expression in such a function raises PG's 42804 when
    executed. PG raises it at CREATE; see Residuals.
- **SQL functions.**
  - The CREATE check accepts several columns for a record or composite
    result.
  - `$n` is checked against the input count.
  - At run time, several outputs make the result `recordText(row)`, in the
    non-set path and in the set path alike.
- **Record text.**
  - `recordFieldText` / `recordText` (expr.go) implement record\_out's
    quoting, doubling quotes and backslashes.
  - `evalRowExpr` now uses them, so `ROW(…)` output changes the same way.
  - `decomposeCompositeText` parses with record\_in's tokenizer
    (`parseRecordText`).
- **Result columns.** `catalog.RoutineResultColumns` lists the columns a
  FROM-clause call produces: the outputs, else the composite type's columns,
  else one column named after the alias. It was moved from the optimizer.
  - The planner's binding (`userRoutineColumnSchema`) uses it.
  - So does the analyzer's scope (`userFuncColumns`). The analyzer skips the
    same built-ins `planTableFuncRangeVar` dispatches first, so the two always
    agree.
- **Field selection.** `outParamRecordFields` gives `(f(…)).col` the outputs
  as fields when the call's result is a record.
- **Arity.** `Routine.NumInputArgs` (pronargs) is used for the target-list
  SRF match and the field-select candidate. `bundleVariadicArgs` counts input
  positions only, so an OUT parameter declared before the VARIADIC one no
  longer shifts it (`f(OUT n int, VARIADIC xs int[])` matches PG).
- **pg\_proc.**
  - `pgProcInputArgOIDs` feeds `proargtypes` / `pronargs` in both the view
    and the heap row, and the heap's (proname, proargtypes, pronamespace)
    index key.
  - The heap row now carries `proallargtypes` (oid\[\]) and `proargmodes`
    when any mode is not IN.
  - `buildFunctionArguments` prints IN for procedures only.

## Verification

- **PG 18.3 probes.**
  - `probe.sql` is identical to PG except for the default
    arguments of `f7` (M0146-0167) and `prorettype::regtype` printing 2249
    (ledgered).
  - `probe2.sql` is identical to PG except for the CREATE-time RETURN check.
- **New test.** `TestOutParameterFunctions` (postmaster) fails 21 arms on
  HEAD.
- **Corrected test.** `TestPgGetFunctionIdentityArgumentsOutMode` expected
  `IN x integer, …`. PG 18.3 gives `x integer, OUT y integer`.
- **Regress A/B against HEAD.**
  - `rangefuncs` 2565 → 2295 lines, `plpgsql` 4086 → 4029, `polymorphism`
    2242 → 2236.
  - `opr_sanity` is 1788 → 1788 when run alone. In the sequence it moved +5,
    because more functions from earlier cases now exist.
  - The other 12 cases are identical, `rowtypes` included.
- **Gates.** Units (parser goldens regenerated); tpch-spotcheck; the TPC-DS
  fire-set gate. The acceptance arm and sf025 are run after the nightly batch.

## Residuals

- **M0146-0167.** User-function DEFAULT arguments are never applied: `f(1)`
  for `f(a int, b int DEFAULT 2)` raises "function does not exist". This is
  independent of OUT parameters.
- **M0146-0168.** A non-SETOF SQL function whose final query returns several
  rows raises 21000. PG returns the first row.
- **Ledgered.**
  - `regtype` output and input do not know the pseudo-types `record`,
    `anyelement` and `"any"`: `2249::regtype` prints `2249`.
  - PL/pgSQL is not compiled at CREATE, so "RETURN cannot have a parameter in
    function with OUT parameters" is raised at the call.
  - RETURNS TABLE columns keep mode `o`, where PG uses `t`.
  - A single-column RETURNS TABLE keeps prorettype record, where PG uses the
    column's type.
