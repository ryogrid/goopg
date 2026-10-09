# M0146-0124 — COALESCE / NULLIF / GREATEST / LEAST and null tests over constants fold

Status: done 2026-10-09 (69bf83609). Parent: M0146-0123.

## Problem

M0146-0123 folded immutable function calls. A follow-up probe against PG
18.3 found the remaining `eval_const_expressions` arms that goopg lacked:

| query fragment | PG 18.3 | goopg before |
|---|---|---|
| `COALESCE(NULL, 5)` | `5` | `COALESCE(NULL, 5)` |
| `COALESCE(NULL, b, NULL, 7)` | `COALESCE(b, 7)` | unchanged |
| `COALESCE(4, b)` | `4` | unchanged |
| `NULLIF(1, 2)` | `1` | unchanged |
| `GREATEST(1, 3, 2)` | `3` | unchanged |
| `b IS NULL AND 1 IS NULL` | `One-Time Filter: false` | a Filter over the scan |

Regress `case.sql` witnesses the gap in its "constant subexpression
simplification" block. `NULLIF(1, 2) = 2`, `NULLIF(1, 1) IS NOT NULL` and
`NULLIF(1, null) = 2` all plan `Result  One-Time Filter: false` in PG.
goopg gated a Seq Scan on the unfolded expression, so `case.sql` diverged
(15 diff lines).

## PG behaviour (clauses.c, eval_const_expressions_mutator)

- **T_CoalesceExpr.** NULL Consts are dropped. A non-null Const is the
  result when no argument precedes it; otherwise it ends the list. An
  all-NULL list becomes a NULL Const of the COALESCE type. A single
  remaining non-constant argument stays a one-argument COALESCE.
- **T_NullIfExpr.** If either argument is a NULL Const, the result is the
  first argument. Two Consts are evaluated.
- **T_MinMaxExpr.** Generic arm: when every argument is a Const, the
  expression is evaluated, ignoring NULLs.
- **T_NullTest and T_BooleanTest.** Over a Const, the result is a bool
  Const. The row-valued NullTest splits per field (not ported).

## Change

All in `internal/optimizer/foldconst.go`.

- `FoldConstants` gains `IsNullExpr` and `IsBoolExpr` arms. The function
  previously did not even descend into them.
  - `constantNullness` treats `NullConst` and a cast over it as NULL, and a
    plain literal as non-null.
  - `foldBooleanTest` applies the executor's truth table.
- `simplifyConditionalCall` handles goopg's FuncCall spelling of COALESCE /
  NULLIF / GREATEST / LEAST, before `tryFoldFuncCall`.
- **Type gate.** PG coerced every argument to the common type during parse
  analysis; goopg does not. So every arm folds only when all non-null
  arguments carry one exact type and none is an untyped string literal. The
  kept argument then already has the expression's type, so neither a value
  nor a column type can change.
- **Typed NULL.** `NULLIF(1, 1)` yields a non-explicit `CastExpr` over
  `NullConst` with the first argument's type.

## Verification

- **Test.** `TestFoldConstantsConditionalAndNullTests` covers every arm,
  the typed NULL, and the declining cases: mixed types, untyped strings, a
  non-constant GREATEST, and an all-NULL COALESCE.
- **Scratch probe vs PG.** Values, `pg_typeof` and plans are equal for
  every probed form. The one exception is the VERBOSE `Output:` list, a
  pre-existing rendering gap.
- **Regress A/B** (32 cases). `case.sql` goes from 15 diff lines to 0
  (100%); `join` shows only its known row-order flip.
- **TPC-DS fire set.** No plan changed. TPC-H plans are byte-identical.
- **Gates.** units, TPC-H spotcheck, acceptance arm, fire set, SF0.25 sweep
  (96/96) and ea-ratchet (7) all PASS.

## Found on the way (S2, filed for owner placement)

- **M0146-0125.** `array || NULL` and `NULL || array` return NULL; PG
  returns the array. The array concatenation functions are not strict.
- **M0146-0126.** COALESCE does not resolve its arguments' common type, so
  `coalesce(1, 2.5) / 3` is `0` (integer division) where PG gives
  `0.33333333333333333333`.

## Not covered (ledgered)

- Mixed-type arguments, which need the parse-time coercion goopg does not
  record.
- An all-NULL COALESCE, which needs a NULL of the common type.
- Row-valued NullTest splitting.
- Strict operators over a typed NULL. `a = NULL::int` still prints
  `Filter: (a = (NULL)::integer)`, where PG folds it to a NULL Const and a
  `One-Time Filter: false`.
