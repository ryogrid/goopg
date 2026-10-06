# M0146-0062 — an integer literal that fits in 32 bits is int4

Status: done 2026-10-06 (`6fa8b77c9`, `898d5342c`).

## Symptom

- goopg typed every integer literal `int8`, so int4 arithmetic never
  overflowed:
  - `select 2147483647 + 1` returned `2147483648`, where PG 18.3 raises
    `integer out of range`;
  - `select pg_typeof(9998)` printed `bigint`, where PG prints `integer`;
  - a VALUES or derived-table column built from such literals was
    `bigint`.
- Through a correlated sub-select, `(SELECT pg_typeof(v.x))` printed
  `unknown`.

## PG behaviour

- `make_const` (`postgres/src/backend/parser/parse_node.c`) types an
  integer literal `int4` when it fits in 32 bits and `int8` otherwise.
- gram.y's `doNegate` folds the minus sign into the literal, so
  `-2147483648` and `-(2147483648)` are `int4`.
- Constant folding runs the operator for the operand types, so
  `2147483647::bigint + 1` stays `int8`, while `2147483647 + 1` is
  `int4 + int4` and raises.
- `sum(int2)` and `sum(int4)` return `int8`.

## Change

- `IntegerConst` gains a `Wide` flag. `IntegerConstIsInt8` is true when
  the flag is set or the value lies outside int32. `exprType` and
  `expr_result_type.go` type the constant from it.
- Constant folding (`foldconst.go`):
  - `evalArith` folds int64 arithmetic in `evalIntArith`. When neither
    operand is int8 and the result leaves int32, it raises `22003 integer
    out of range`.
  - A folded constant from int8 operands keeps `Wide`.
  - Negation keeps `Wide` only when the operand had it explicitly. That
    matches doNegate.
- Runtime overflow is already checked by `overflowCodeForType` on
  `BinaryOp.ResultType`. That type now comes out `int4` for
  `int4_col + 1`, so the check fires.
- `sumResultType` widens `sum(int2/int4)` to `int8` for both aggregate and
  window sum. Before this change the int8 literal hid the missing rule.
- The hypothetical-set "function … does not exist" message formats
  argument types through `catalog.ArgTypeDisplayAlias` (`integer`, not
  `int4`). An int8-literal workaround there was removed.
- `exprType` gains an `OuterColumnRef` arm that returns the reference's
  column type. That fixes the correlated `pg_typeof` witness and the
  overflow check on correlated arithmetic.

## Verification

- `TestIntegerLiteralIsInt4WhenItFits` (`internal/executor`): every
  expected value is PG 18.3 output. It covers pg_typeof of literals,
  negation, VALUES, column arithmetic, int8-widened folding, `/` and `%`,
  five overflow errors and the correlated reference. Eight assertions
  failed at the pre-fix HEAD.
- Gates for both commits:
  - units PASS;
  - tpch-spotcheck PASS;
  - acceptance arm 24/24 MATCH;
  - sf025 96/96 values;
  - ea-ratchet PASS.
- Fire set, first commit: Q21, Q35, Q48, Q49, Q54, Q75, Q78 and Q83
  fired, all width-only (`sum(int4)` is now 8 bytes; Q48's width now
  equals PG's). PLAN-PARITY match and text-identical counts are unchanged.
- Fire set, second commit: nothing fired.
- Regress A/B against HEAD, first commit, after the sum and display-name
  fixes:
  - aggregates and with are identical to the baseline;
  - partition_prune moved by −4 lines, which is neutral: a
    Filter-over-Append shape that differs from PG either way.
- Regress A/B against HEAD, second commit:
  - join 15595 → 15557 mismatch lines;
  - subselect 1665 → 1663;
  - plpgsql moved within its known flap.

## Not covered

- `sum(int8)` still returns `int8`. PG returns `numeric`.
- `pg_typeof` does not evaluate its argument, so `pg_typeof(a + 1)` on an
  overflowing row prints a type where PG raises.
- M0146-0012 slice 2 kept the one-relation rule's correlated half because
  outer keys from literal columns were `int8` against `int4` index
  columns. Those keys are `int4` now, so the rule may be removable. Filed
  as M0146-0073.
