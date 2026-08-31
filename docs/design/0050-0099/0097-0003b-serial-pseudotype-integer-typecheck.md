# 0097-0003b — SERIAL pseudo-types type-check as their integer base

Status: accepted
Milestone: M0097-0003 (Core standalone + scalar type parity); surfaced while
triaging `copyselect` (M0097-0024).

## Problem

`SERIAL` / `BIGSERIAL` / `SMALLSERIAL` are not real PostgreSQL types — they are
shorthand that `CREATE TABLE` expands to `integer` / `bigint` / `smallint`
columns plus an owned sequence and a `nextval(...)` default. `pg_typeof` of a
serial column reports `integer`.

goopg stores the column's catalog type as the literal string `"serial"`
(the INSERT auto-increment path in `internal/executor/operators_storage.go`
keys off that name to pick the right sequence). The runtime/codec layer already
treats `serial` as an alias for `int4` (`internal/executor/codec.go` lists
`"int4", "integer", "int", "serial"` together), so reading and displaying
serial values works and `pg_typeof` returns `integer`.

The **analyzer and planner type systems**, however, did not know the SERIAL
aliases, so any expression mentioning a serial column failed type-checking:

```
select t from test1 where id = 1;   -- id serial
ERROR:  operator = has incompatible operand types "serial" and "int8"

select id + 1 from test1;
ERROR:  operator + requires numeric operands
```

This blocks any query that compares or does arithmetic on a serial column with
an integer literal — extremely common (`WHERE id = N` on a serial PK). It is
also the first blocker in the `copyselect` regress test
(`select t from test1 where id = 1 UNION …`).

Plain `int`/`integer` columns were unaffected because `isNumericTypeName`
already lists them.

## Root cause

Three predicates omitted the SERIAL aliases:

- `internal/analyzer/analyzer.go` `isNumericTypeName` — gates **both**
  comparison (`isComparable` → both-numeric) and arithmetic
  (`isNumericLike` → `isNumericTypeName`). With `"serial"` absent it returned
  false, so `serial = int8` failed comparability and `serial + int8` failed the
  numeric-operand check.
- `internal/planner/planner.go` `isIntegerLikeType` — used by `exprType` to
  decide whether a `BinaryOp`'s operands take integer promotion.
- `internal/planner/planner.go` `promoteIntType` — computes the arithmetic
  result type (serial→int4, bigserial→int8, smallserial→int2).

Note: the planner's `isNumericTypeName` means specifically the
NUMERIC/DECIMAL arbitrary-precision family and was intentionally left
untouched (serial is integer, not numeric).

This is the recurring sibling-path divergence class
([[pattern_sibling_paths_must_agree]]): the codec path canonicalised serial→int4
but the type-checking path did not.

## Fix

Add `"serial"`, `"bigserial"`, `"smallserial"` as integer aliases in the three
predicates, mirroring the existing alias-list style used throughout
`codec.go`. The change is purely additive — these predicates previously returned
`false` / fell through for serial, so only previously-erroring cases change
behaviour. The stored catalog type stays `"serial"` (INSERT auto-increment
unaffected); only the type-checking view of it becomes integer, matching
`pg_typeof`.

Mapping: `serial`↔`int4`, `bigserial`↔`int8`, `smallserial`↔`int2`.

## Verification

- New unit tests:
  - `TestSerialPseudotypeIntegerTypeCheck` (`internal/analyzer/coerce_test.go`)
    — serial/bigserial/smallserial comparison + arithmetic against integer
    literals and each other pass; serial-vs-`text` still errors (guards against
    over-broadening).
  - `TestPromoteIntTypeSerialFamily` (`internal/planner/planner_test.go`) —
    `isIntegerLikeType` recognises the aliases and `promoteIntType` resolves
    serial→int4, bigserial→int8, smallserial→int2 (with mixed-width promotion).
- Live server (port 5533): `select t from test1 where id = 1`,
  `select id+1 from test1`, `select t from test1 where id > 2`, and
  bigserial/smallserial comparisons all succeed (were hard errors before).
- Gates green: `internal/analyzer`, `internal/planner`, `internal/server`
  pass; `internal/executor` has one **pre-existing, unrelated** failure
  (`TestAnalyzeRespectsStatsTarget`, NDistinct sampling estimate 398 vs 400 —
  fails identically on HEAD without this change).

## Scope / not done

- The `copyselect` regress test is still blocked by a **separate** issue: the
  parser greedily attaches a trailing `ORDER BY 1` to the right-hand set-op
  branch (`select * from v_test1 ORDER BY 1`), where positional `1` resolves to
  the `*` target and raises `42601 "'*' is not allowed here"`. Per SQL grammar
  the trailing ORDER BY binds to the whole set operation, not the right operand.
  That parser fix is the next `copyselect` task (see M0097-0024 notes).
- The deeper "normalise serial→int4 in the stored catalog type" is intentionally
  **not** done: the INSERT auto-increment path and several codec switches key
  off the literal `"serial"` name, so changing storage is broad/risky. Treating
  serial as integer in the type system is the contained, correct-for-analysis
  fix.
