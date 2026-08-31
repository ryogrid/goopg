# VARIADIC function call-site argument collapsing (M0119-0004 DU-002 follow-up)

Status: accepted
Date: 2026-07-15

## Problem

`CREATE FUNCTION sum_variadic(VARIADIC arr integer[]) RETURNS integer ...`
followed by `SELECT sum_variadic(1, 2, 3)` failed with `function
sum_variadic(integer, integer, integer) does not exist`, even though the
routine's stored signature (`Routine.ArgTypes`/`ArgModes`) was already
correct — confirmed live via `pg_get_function_identity_arguments`, both
before and after the sibling VARIADIC-array *signature-matching* fix landed
in `4b09f5fa` (`.ralph/deferral_ledger.md`, 2026-07-15 row).

This is a materially different mechanism from that sibling fix: `4b09f5fa`
made ALTER/DROP/COMMENT FUNCTION's *identity resolution* correctly compute
an array-suffixed signature key. This document covers *call resolution* —
matching a `SELECT`/expression-context function invocation's positional
argument list against a routine that declares a trailing VARIADIC
parameter, and collapsing the caller's N arguments into the one array value
the routine body actually receives.

## Root cause

`internal/executor/plpgsql_runtime.go`'s `resolveRoutineOverload` (the sole
call-resolution path for `evalStoredRoutineFuncCall`, used whenever an
expression evaluates a user-defined-function call) required an exact
argument-count match:

```go
if len(c.ArgTypes) != len(args) {
    continue
}
```

For `sum_variadic`, `len(c.ArgTypes) == 1` (the single `VARIADIC arr
integer[]` parameter) but `len(args) == 3` for a `sum_variadic(1, 2, 3)`
call, so the routine was excluded from the candidate set before any
type-compatibility check ran — the function looked like it didn't exist at
any argument count other than exactly 1.

This is the call-resolution counterpart to `internal/executor
/operators_call.go`'s `callOp.Open` (the `CALL <procedure>(...)` statement
path), which already implements VARIADIC-aware count matching and argument
bundling (M0097-0022) — but `CALL` and a function invoked via `SELECT`
resolve through two entirely separate code paths, and only the `CALL` path
had ever been given VARIADIC support.

## Fix

Two new helpers in `internal/executor/plpgsql_runtime.go`, alongside
`resolveRoutineOverload`:

- **`callArgTypesForCandidate(c *catalog.Routine, n int) ([]catalog.Type,
  bool)`** — computes the per-position parameter types to type-check an
  `n`-argument call against candidate `c`. When `c` declares no VARIADIC
  parameter, behavior is unchanged (exact-length match against
  `c.ArgTypes`). When `c`'s last parameter mode is `"v"` (PostgreSQL
  requires VARIADIC to always be the last parameter —
  `functioncmds.c:interpret_function_parameter_list`), any `n >=
  variadicPos` is accepted; the returned type slice repeats the VARIADIC
  parameter's *element* type (its declared array type name with the
  trailing `"[]"` stripped) for every position at or past `variadicPos`,
  since `Routine.ArgTypes[i].Name` bakes the array suffix directly into the
  string (the same "IsArray is never consulted" storage convention
  `4b09f5fa` documented).
- **`bundleVariadicArgs(argModes []string, args []Datum) []Datum`** —
  called once `resolveRoutineOverload` has picked a routine, before
  dispatch. Collapses every argument at and after the VARIADIC position
  into a single array-valued `Datum` via the existing `buildArrayDatum`
  helper (shared with `callOp`'s bundling code), so `args` becomes
  positionally parallel to `r.ArgTypes` again — required because every
  dispatch path (`executeSQLRoutine`, `executePLpgSQLRoutine`) binds
  `args[i]` against `r.ArgTypes[i]` by direct index with no VARIADIC
  awareness of its own.

`resolveRoutineOverload` now calls `callArgTypesForCandidate` instead of the
inline exact-length check, and `evalStoredRoutineFuncCall` calls
`bundleVariadicArgs` on the resolved routine's `ArgModes` immediately before
`executeStoredRoutine`. The `IsProcedure` "use CALL, not SELECT" error
branch in `evalStoredRoutineFuncCall` also gained an index guard
(`i < len(r.ArgTypes)`, falling back to the VARIADIC parameter's type for
excess positions) since this change is the first way that branch can be
reached with `len(x.Args) > len(r.ArgTypes)`.

## Scope and non-goals

- Only the scalar function-call path (`evalStoredRoutineFuncCall`, the sole
  caller of `resolveRoutineOverload`) is touched. `callOp.Open` (`CALL
  proc(...)`) already had its own, separately-implemented VARIADIC
  handling and is unchanged — a follow-up could deduplicate the two
  bundling implementations onto one shared helper, but they are functionally
  independent today and this change does not touch the already-tested `CALL`
  path.
- Explicit `VARIADIC arr => ARRAY[...]` call syntax (passing the array value
  directly with the `VARIADIC` call-site keyword, letting the caller opt out
  of collapsing) is not handled — same as the pre-existing `CALL` path,
  which has never implemented it either. Not exercised by any known test or
  deferral-ledger item; record as a follow-up if it surfaces.
- Overload disambiguation among multiple same-name routines where one is
  variadic and another is a plain fixed-arity match with the same argument
  count is not specially prioritized — `routineArgsExactMatch`/
  `hasPolymorphicArgType` still operate on the original `c.ArgTypes` (not
  the per-call expanded slice), so a variadic candidate rarely wins an
  exact-match tie-break. Not exercised by the immediate bug (`sum_variadic`
  has a single overload); left as-is rather than expanding this change's
  scope.

## Verification

New `internal/executor/variadic_call_test.go`:

- `TestVariadicFunctionCallCollapsesArgs` — a `LANGUAGE plpgsql` VARIADIC
  function called with 0, 1, 3, and 5 positional arguments, asserting
  `array_length(arr, 1)` sees the correctly collapsed array each time
  (including the zero-argument `NULL` case, matching real PostgreSQL's
  `array_length('{}'::int[], 1)` semantics).
- `TestVariadicFunctionCallSQLLanguage` — the same collapsing behavior
  through `executeSQLRoutine` (the sibling `LANGUAGE sql` dispatch path to
  `executePLpgSQLRoutine`, both of which bind `args[i]` to `r.ArgTypes[i]`
  positionally and therefore both needed the bundled array).

Gates: `go build ./...`/`go vet ./...` clean repo-wide; `go test
./internal/executor/... ./internal/catalog/... ./internal/parser/...`
PASS; `go test -short $(go list ./... | grep -v /internal/testport)` (full
repo, short mode) 0 FAIL; `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33);
`RALPH_PRECOMMIT_SCOPE=smoke bash scripts/ralph-precommit-test.sh` PASS (0
failed, all 3 workloads).

## Deferral ledger

Resolves the "VARIADIC call-site argument matching... never implemented"
open item recorded in the 2026-07-15 VARIADIC-array signature-matching
ledger row (`.ralph/deferral_ledger.md`).
