# 0051-0003 — Implicit type coercion

**Status:** draft
**Date:** 2026-05-04
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

## Plan

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
