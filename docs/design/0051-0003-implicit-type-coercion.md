# 0051-0003 — Implicit type coercion

**Status:** accepted
**Date:** 2026-05-05
**Milestone:** 0051 — Planner expression-level improvements
**Supersedes:** —

## Context

`int < numeric` errors today with "type mismatch". `int2 + int4` likewise
fails. Upstream silently coerces both sides to the common type via a
fixed lattice. Without coercion, every TPC-H-style query forces explicit
`CAST(...)` calls.

The lattice (subset that matters for goopg's current type set):

```
int2 → int4 → int8 → numeric → float4 → float8
text ↔ varchar ↔ char(n)
date → timestamp → timestamptz
```

When the analyzer can't find an exact-match operator for `(L, R)`, it
walks each side along the lattice until they agree, picking the
"shorter total walk".

## Implementation (landed 2026-05-05)

### New file: `internal/analyzer/coerce.go`

Three exported helpers:

1. **`NumericCoercePrecedence(typeName string) int`** — maps a type name to its
   position on the numeric coercion lattice (`int2=0, int4=1, int8=2, numeric=3,
   float4=4, float8=5`). Returns -1 for non-numeric types. Type aliases
   (`integer`, `smallint`, `bigint`, `decimal`, `real`, `double precision`) are
   mapped to their canonical positions.

2. **`PromoteNumericType(l, r catalog.Type) catalog.Type`** — returns the wider
   of two numeric types. `unknown`-typed literals yield to the concrete side.
   Returns empty Type when either side is non-numeric (caller handles the error).

3. **`PromoteStringType(l, r catalog.Type) catalog.Type`** — returns `text` for
   mixed string-family types (`text ↔ varchar ↔ char ↔ bpchar`), with
   unknown-type yielding to the concrete side.

4. **`PromoteTimestampType(l, r catalog.Type) catalog.Type`** — returns the
   wider of `date` (0) → `timestamp` (1) → `timestamptz` (2).

### Modified: `internal/analyzer/analyzer.go` — BinaryOp arithmetic branch

For `+`, `-`, `*`, `/`, `%` operators the result type is now computed via
`PromoteNumericType(leftTyp, rightTyp)` instead of blindly returning `leftTyp`.
This ensures that:

| Left   | Right   | Result (before) | Result (after) |
|--------|---------|-----------------|----------------|
| int8   | numeric | int8 ✗          | numeric ✓      |
| int4   | int8    | int4 ✗          | int8 ✓         |
| numeric| float8  | numeric ✗       | float8 ✓       |
| unknown| numeric | unknown         | numeric ✓      |

For `||` (string concat): now uses `PromoteStringType` to return the correct
wider string type (`text`) for mixed `text`/`varchar`/`char` operands.

### Key design decision: no CastExpr wrapping

The design doc plan called for wrapping operands with `CastExpr`. This was not
implemented because:
1. CastExpr is a no-op in the planner v0 (passes through the inner expression).
2. The executor's `promoteCrossKind` already handles runtime mixed-numeric
   evaluation correctly.
3. The gap was purely in the **reported result type** from the analyzer, not
   in runtime semantics. Fixing `PromoteNumericType` in the result type path
   closes the gap without touching the planner or executor.

### Tests: `internal/analyzer/coerce_test.go` (9 new tests)

- `TestNumericCoercePrecedence`: lattice ordering + all type aliases.
- `TestPromoteNumericTypeMatrix`: DoD matrix — all 6×6 pairs of numeric types
  return the wider type.
- `TestPromoteNumericTypeUnknown`: unknown/untyped operands yield to concrete.
- `TestPromoteStringType`: text/varchar/char all promote to text.
- `TestBinaryOpArithmeticResultTypes`: spot-checks that `SELECT 1 + 1.5` etc.
  parse and analyze without error.
- `TestBinaryOpCrossNumericComparisons`: all 6×6 pairs × 6 comparison operators.
- `TestBinaryOpCrossNumericArithmetic`: all 4×4 integer/numeric pairs × 4
  arithmetic operators.
- `TestInvalidCrossTypesStillError`: `text + int` and `int = text` still error.
- `TestStringConcatMixedTypes`: text/varchar/char concat combinations all pass.

### Original Plan

1. New helper `internal/analyzer/coerce.go::FindCommonType(L, R) (T,
   error)`. Uses the upstream-shaped lattice table.
2. New helper `WrapCoerce(expr Expr, target Type) Expr` — emits a
   `CastExpr` if needed; no-op if `expr.Type() == target`.
3. Analyzer's binary-op resolution:
   - Look up `(L, R)` exact match. If found, use it.
   - Otherwise call `FindCommonType(L, R)`. If success, wrap each side
     with `WrapCoerce` and look up `(common, common)`.
   - Otherwise error with the existing "type mismatch" SQLSTATE 42883.
4. Same logic for function calls: try exact-match, then climb each arg
   independently up the lattice.
5. The constant-folder (0051-0002) runs *after* coercion, so
   `1 + 2.0` becomes `1::numeric + 2.0::numeric` then folds to
   `3.0::numeric`.

## Definition of Done

- TPC-H queries that today require explicit `CAST` to compile run
  unchanged after dropping the casts.
- Matrix test covering each `(numeric|integer|smallint|real|double
  precision)` × each binary operator.
- Bad pairs (e.g. `text + integer`) still error with 42883.

## Upstream reference

- `postgres/src/backend/parser/parse_coerce.c` —
  `select_common_type`, `coerce_to_specific_type`,
  the implicit-cast lattice (`pg_cast` rows).
- `postgres/src/backend/parser/parse_func.c`,
  `parse_oper.c` — function/operator resolution.

## goopg references

- `internal/analyzer/types.go`,
  `internal/analyzer/operators.go`.
- `docs/design/root-0011-planner.md`,
  `docs/design/0003-0012-numeric-arithmetic.md` —
  current numeric coercion (subset).
