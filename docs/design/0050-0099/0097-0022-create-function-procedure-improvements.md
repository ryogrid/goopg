# M0097-0022: CREATE FUNCTION/PROCEDURE Improvements

## Summary

Reduced `create_function_sql` regress diffs from 134→121 and `create_procedure`
from 54→29 by implementing a wide range of function/procedure improvements.

## Changes

### DROP SCHEMA CASCADE – Multi-overload Fix
`execDropCompat` was calling `rs.DropByName(...)` which returns `ErrRoutineAmbiguous`
when multiple overloads share a name (e.g. `test1(int)` and `test1(anyelement)` both
in `temp_func_test`). The result was silently ignored so neither function was dropped,
causing cross-test contamination into `create_procedure`. Fixed by calling `rs.Drop(...,
r.ArgTypes)` which drops the specific overload by signature.

### RETURNS SETOF VOID
`evalSQLFunctionSetof` now returns `nil, nil` immediately when the declared return type
is `void`, producing 0 rows. Previously it executed the body and returned actual rows
(e.g. `generate_series` results for `voidtest5`).

### SQL Function CONTEXT Messages
`executeSQLRoutine`, `evalSQLFunctionSetof`, and `executeSQLProcedureCore` now wrap
runtime errors with `wrapSQLFunctionContext(err, funcName, stmtNum)` so errors surface
as `CONTEXT: SQL function "name" statement N`.

### information_schema Virtual Tables
Added `information_schema.parameters` and `information_schema.routines` virtual tables
that read from the routine registry. Also added stub `routine_routine_usage`,
`routine_sequence_usage`, `routine_column_usage`, and `routine_table_usage` tables
(returning 0 rows). Parameter defaults for string-typed params get `::typename` cast
annotation matching PostgreSQL's canonical form (e.g. `'foo'::text`).

### sum(integer) is not a procedure
`buildTypedArgListStr()` infers argument types from parser-level expressions. Built-in
functions called via CALL now report typed arg lists in the error message (`sum(integer)`
not `sum(unknown)`).

### VARIADIC Parameter Parsing
`parseProcedureArg` and `parseFunctionArg` now accept `name mode type` form in addition
to `mode name type` form (PostgreSQL allows both). Fixes parsing of
`CREATE PROCEDURE ptest11(a OUT int, VARIADIC b int[])`.

### VARIADIC Argument Bundling for CALL
`callOp.Open` now bundles excess arguments (beyond the last VARIADIC position) into a
text-format array datum `{e1,e2,...}` at the VARIADIC parameter slot.

### array_subscript Type Inference
`exprType` in the planner and `analyzeExpr` in the analyzer now return `"unknown"` for
`array_subscript` when the array's base type is unknown (e.g. `$2[1]` where `$2` is a
parameter). This allows arithmetic like `$2[1] + $2[2]` to type-check correctly.

### Procedure Validation Errors
- `WINDOW` attribute on `CREATE PROCEDURE`: raises `"invalid attribute in procedure definition"`
- `STRICT` attribute on `CREATE PROCEDURE`: same
- `STRICT` attribute on `ALTER PROCEDURE`: same
- `VARIADIC` must be the last parameter (checked at create time)
- `OUT` parameters cannot appear after a default IN parameter

### ALTER FUNCTION/PROCEDURE/ROUTINE RENAME TO
Parser now captures the new name from `RENAME TO new_name` into `AlterFunctionStmt.RenameTo`.
`execAlterFunction` routes to `rs.RenameRoutine(r, newName)` which atomically updates both
`byKey` and `byName` indices. `Routines.RenameRoutine` is a new catalog method.

### ALTER ROUTINE Kind Check
`ALTER ROUTINE` now skips the function/procedure kind check, allowing it to operate on
both functions and procedures (mirrors PostgreSQL's `ALTER ROUTINE` semantics).

### CALL Type-Based OUT Param Matching
`callOp.Open` now infers argument expression types without evaluating them and uses these
for OUT parameter type compatibility checks. This fixes `CALL ptest9(1./0.)` which should
fail with `"procedure ptest9(numeric) does not exist"` because `1./0.` has type numeric.

### "is a procedure" Hint
`executeStoredRoutine` now adds `HINT: "To call a procedure, use CALL."` when reporting
that a routine called via SELECT is a procedure.

### Routine OID in information_schema specific_name
`specific_name` uses `routineName_OID` format to ensure uniqueness across overloads.
