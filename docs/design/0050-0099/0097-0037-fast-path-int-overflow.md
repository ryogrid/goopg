# 0097-0037 — Fast-path expression evaluator: restore int2/int4 overflow detection

Status: accepted
Date: 2026-05-24
Area: `internal/executor` (compiled expression evaluator)

## Problem

The M0097-0019 audit refresh (commit `bf9c791`) re-ran the full
`TestPort_RegressSuite` after M0111 and found 6 pg_regress cases that had been
`pass` at M0097-0003 (2026-05-13) but now `failed`: `int2`, `int4`, `name`,
`numerology`, `portals_p2`, `select_implicit`. That commit attributed the
regression to the M0106-0010 PG-native physical-tuple codec switch.

Root-cause analysis shows the attribution was wrong for the integer cases. The
real cause is the **compiled fast-path expression evaluator** introduced by
M0107-0003 (`internal/executor/exprnode.go`, "Phase C.3"). That evaluator
compiles `planner.Expr` trees into a flat `[]ExprNode` slab and dispatches via
an integer kind-switch (`evalFastExpr`) to avoid interface-assertion overhead on
the hot path. The `ExprBinaryOp` fast path called `evalBinary(op, left, right)`
directly but **omitted the integer-overflow range check** that the interpreted
path (`evalExprSlot`, `expr.go`) applies after integer arithmetic — and the
compiled node did not even carry the operation's `ResultType`.

Consequence: a projected expression such as `int2 '32767' * int2 '2'` or
`int4col * 2` silently returned the wrapped/out-of-range value (e.g. `65534`)
instead of raising `ERROR: smallint out of range` / `integer out of range`.
`pg_regress` `int2`/`int4` exercise exactly these overflow cases, so they
diverged. (`pg_typeof(...)` of the same expression took the `pg_typeof`
function path, which routes its argument through `evalExprSlot`, so the
overflow *did* fire there — masking the bug in casual inspection.)

The interpreted path still overflowed correctly; only the compiled projection
path was affected. This is why the failure looked like a codec regression: it
appeared only end-to-end, not in expression unit tests that used the
interpreted path.

## Fix

`internal/executor/exprnode.go`:

1. `ExprBinaryOp` nodes now carry an **overflow code** in `payload[1]`
   (`ovfNone` / `ovfInt2` / `ovfInt4`), derived from the planner
   `BinaryOp.ResultType` at compile time via `overflowCodeForType`.
2. `evalFastExpr`'s `ExprBinaryOp` case applies the same int2/int4 range check
   as `evalExprSlot` after `evalBinary`, returning `ExecError` code `22003`
   ("smallint out of range" / "integer out of range") on overflow.
3. **Float-typed** `BinaryOp`s (`float4`/`float8`/`real`/`double precision`)
   now compile to `ExprAdapter` (fall back to `evalExprSlot`). The fast path's
   exact integer/decimal arithmetic diverges from `evalExprSlot`'s `float64`
   path (which formats with `%.15g` to match PostgreSQL `float8out`); routing
   floats through the interpreted path keeps output parity and is strictly more
   correct than before.

`int8`/`bigint` arithmetic keeps the fast path with no range check, matching
`evalExprSlot` (which also does not detect int8 overflow). Expressions with an
empty/unresolved `ResultType` keep the fast path unchanged (`ovfNone`).

The change preserves the M0107 hot-path optimisation for the common
integer-arithmetic case: the overflow check is two inline comparisons gated on
`result.Kind == KindInt`, no new allocation, no interface assertions.

## Verification

- New unit tests in `internal/executor/phase_c_test.go`:
  `TestEvalFastExprIntOverflow` (8 subcases: int2/int4 mul/add/sub overflow +
  in-range + int8/untyped pass-through) and `TestBuildExprFloatFallsBackToAdapter`.
- `go test -count=1 ./internal/executor/ ./internal/planner/ ./internal/server/`
  green (except pre-existing `TestAnalyzeRespectsStatsTarget`, a stats-sampling
  test that fails independently of this change — verified by stashing this
  change and re-running).
- pg_regress parity (`TestPort_RegressSuite`): **`int4`, `portals_p2`,
  `select_implicit` now pass**; `int2` drops from 44 → 4 normalised diff lines.
  The 4 residual `int2` diffs are a *separate, pre-existing* bug: `INSERT INTO
  INT2_TBL VALUES ('100000')` reports `smallint out of range` instead of
  `value "100000" is out of range for type smallint` (an input/cast-message
  difference, present both before and after this change; tracked under M0097
  for a future loop).

## Scope / non-goals

- Does not address `name` and `numerology` (different root causes — RAISE
  NOTICE/SRF and float/DISTINCT formatting respectively).
- Does not change the `int2` INSERT cast-message bug noted above.
- Does not alter `evalExprSlot`; the interpreted path was already correct.
