# M0146-0084 — duplicate parameter names are rejected; a call supplies input parameters only

Status: landed 2026-10-12.
Parent: M0146-0080 (item 2a rank via the M0146-0055 pin).

## Problem

Regress `polymorphism` failed `select testpolym(37)` (polymorphism.sql:927)
and `select * from testpolym(37)` (:930) in some runs of the same binary and
not in others, so A/B diffs flipped between 2272 and 2277 lines. There were
two causes.

1. **Duplicate parameter names were accepted.** PostgreSQL 18.3 rejects every
   definition in the file's "named parameters are not unique" block with
   42P13 `parameter name "a" used more than once`. goopg created them. The
   leftover `testpolym(...)` overloads then decided which function the later
   calls found, depending on what had already run in the session.
2. **A function with an OUT parameter could not be called.** Overload
   resolution compared the call's argument count against every declared
   parameter, OUT ones included. So `testpolym(a int, out a int)` and even
   `f1(a int, out b int)` failed with `function … does not exist`.
   - The SQL and plpgsql bodies also bound arguments positionally against the
     full parameter list.
   - Named-parameter rewriting numbered OUT names as if they were `$n`.

## PostgreSQL's rules

- **Duplicate names.** `interpret_function_parameter_list` (functioncmds.c)
  disallows the same name on two input parameters or on two output
  parameters. A pure input (IN, VARIADIC) and a pure output (OUT, TABLE) may
  share one. The error is positioned at the second parameter.
- **Inputs only.** A call's arguments, `$n` and parameter names refer to the
  input parameters only — IN, INOUT and VARIADIC — in declaration order.
  OUT parameters and RETURNS TABLE columns are results.

## Design

- **`checkDuplicateParamNames`** applies the rule at the start of CREATE
  FUNCTION and CREATE PROCEDURE.
  - It raises 42P13 at the offending parameter's position. The probe's
    carets match PG.
  - RETURNS TABLE columns are stored as trailing OUT arguments, so they
    follow the OUT rule, as TABLE does in PG.
- **`routineInputParams(r)`** returns the input parameters' types and names.
  With no OUT parameter it returns the routine's own slices, which leaves the
  common case unchanged. It feeds:
  - `callArgTypesForCandidate`, including the VARIADIC position counted among
    inputs, and the exact-match tie-break in `resolveRoutineOverload`;
  - the argument binding and `rewriteSQLNamedParams` of `executeSQLRoutine`
    and `evalSQLFunctionSetof`;
  - the argument binding of `executePLpgSQLRoutine` and
    `evalPLpgSQLFunctionSetof`.

  Procedures are untouched: CALL already passes one argument per declared
  position, with OUT placeholders.

## Verification

- **Probes.**
  - The polymorphism.sql:919–931 snippet is byte-identical to PG 18.3 on a
    fresh cluster: four 42P13 errors with PG's carets, then `37` and `37`.
  - SQL functions with OUT after IN and OUT before IN (`$1` binds the input)
    match PG.
- **Determinism.** Regress `polymorphism` measures 2242 lines on two
  consecutive runs, down from 2272 or 2277, with no `testpolym` error left.
- **Other regress.** `rangefuncs` 2579 → 2565 and `plpgsql` 4103 → 4087.
  `create_function_sql`, `create_procedure`, `create_aggregate` and
  `object_address` are identical. `alter_generic` moved by its usual one
  line.
- **Test.** `TestFunctionParameterNamesAndOutArgs` (postmaster) fails on HEAD
  in every arm.
- **Gates.** Units; tpch-spotcheck; the acceptance arm (values identical);
  sf025 PASS=99.

## Residuals

Filed as M0146-0165: OUT-parameter functions are still incomplete.

- `RETURNS` cannot be omitted when OUT parameters exist (a syntax error).
- A plpgsql function does not declare or return its OUT variables
  (`variable "y" does not exist`).
- Several OUT parameters do not yield a record (`(3,x)` / `SELECT *`).
- `pronargs` and `proargtypes` count OUT parameters (PG counts inputs only).
- `bundleVariadicArgs` indexes the VARIADIC position over all parameters.
