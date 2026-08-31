# M0134-0136 — `line.sql` line_in-faithful parsing (PARKED)

**Date:** 2026-08-24
**Status:** contained fix landed, PARKED — matches the M0134-0094 (`box.sql`)
/ M0134-0098 (`circle.sql`) precedent exactly.

## Result

`scripts/pg-regress-runner.sh line` against PG 18.3: diff went from
**754 lines (0% parity)** to **55 lines**, with **zero residual
`^ERROR`/`^-ERROR` mismatches** — every remaining diff line is the already-
ledgered psql LINE-position-echo cross-cutting gap shared with box.sql/
circle.sql (see "Not fixed this loop" below).

## Root cause

`line` was a raw-varlena pass-through with no validation at all — the same
state `box`/`circle` were in before M0134-0094/-0098 landed `parseBoxLiteral`/
`parseCircleLiteral`. Any string, well-formed or not, was stored and echoed
back verbatim; `line '...'` typed-literal syntax didn't parse (`point`/`line`
weren't in the parser's typed-literal keyword whitelist at all); the
`line(point, point)` two-point constructor function had no handler; and
`pg_input_is_valid`/`pg_input_error_info` had no `line` case.

## What landed

1. **`parseLineLiteral`** (`internal/executor/expr.go`) — a faithful port of
   PG's `line_in`/`line_decode`/`path_decode`/`line_construct`/`point_sl`
   (`postgres/src/backend/utils/adt/geo_ops.c:950-1130`). Accepts both input
   shapes line_in accepts:
   - the canonical `{A,B,C}` coefficient brace form (`line_decode`), and
   - any two-point form `path_decode` accepts for lseg/path — bare
     `x,y,x,y`, `(x,y),(x,y)`, doubled `((x,y),(x,y))`, or bracketed
     `[(x,y),(x,y)]` — converted to `A,B,C` via `line_construct` + the
     slope of the two points (`point_sl`).

   Two helper primitives back this, kept separate from `parseBoxLiteral`/
   `parseCircleLiteral`'s existing `pairDecode`/`singleDecode` because line
   needs a capability those don't: **SQLSTATE-differentiated float8
   overflow**. `lineSingleDecode` returns a typed `*ExecError` rather than a
   bare `ok bool` — a token like `1e400` gets PG's dedicated
   `22003 "\"1e400\" is out of range for type double precision"`
   (`float.c:484-489`) instead of colliding with the generic `22P02
   "invalid input syntax for type line"` every other malformed token gets.
   `linePairDecode` builds on it for the two-point form.

   `parseLineLiteral` also reproduces line_in's two semantic-validation
   messages, which are NOT generic syntax errors and line.sql asserts their
   exact text:
   - brace form, `A == 0 && B == 0`: `"invalid line specification: A and B
     cannot both be zero"`
   - two-point form, the two points coincide: `"invalid line specification:
     must be two distinct points"`

2. **Wiring**, mirroring the box/circle chokepoint pattern exactly:
   - `internal/executor/codec.go` `coerceTextLikeDatum` — a `line` column's
     assignment coercion now validates + canonicalizes via
     `parseLineLiteral`/`lineCanonicalText`.
   - `internal/executor/expr.go` `evalTypedStringLit` — `line '...'` typed-
     literal syntax shares the same chokepoint.
   - `pg_input_is_valid`/`pg_input_error_info` (`expr.go` +
     `operators_pg_input_error_info.go`) — new `"line"` cases, the latter
     surfacing `parseLineLiteral`'s exact message/SQLSTATE so
     `pg_input_error_info('{0, 0, 0}', 'line')` matches PG's DETAIL row
     byte-for-byte.

3. **`line(point, point)` constructor** (`evalFuncCall`, `expr.go`) — PG's
   `line_construct_pp`: builds the infinite line through two points, raising
   the same "must be two distinct points" error line_in's two-point form
   raises when the points coincide. Deliberately does **not** set
   `ExecError.Pos` — unlike the typed-literal/column-coercion cases above
   (parse-analysis-time errors, which DO carry a position in real PG), this
   is a genuine execution-time function-call error, and line.sql's expected
   output confirms PG omits the "LINE N:" echo for exactly this case.

4. **Parser whitelist gap** (`internal/parser/select.go`
   `tryTypedLiteral`) — `point`/`line` were entirely absent from the
   SQL-standard `typename 'literal'` grammar whitelist, so `point '(3,1)'`
   didn't parse as an expression AT ALL, not even outside a function call.
   This is a parser-wide gap (confirmed the fix doesn't regress box.sql/
   circle.sql/point.sql/lseg.sql — sizes held steady or improved slightly),
   not scoped to line.sql; it happened to be the blocking prerequisite for
   `line(point '(3,1)', point '(3,2)')` to parse as a function call with two
   typed-literal arguments.

## Not fixed this loop

The residual 55-line diff is a single already-known, already-ledgered
cross-cutting gap (M0134-0094/-0098's ledger rows): PG's parse-analysis-time
literal-cast errors carry a wire-protocol error `Position`, which psql's
client renders as a `"LINE N: ...\n  ^"` echo; `coerceTextLikeDatum` never
attaches `ExecError.Pos`, so goopg's error responses omit it. Fixing this
touches every `coerceTextLikeDatum` caller across the INSERT/UPDATE/COPY
pipeline — out of scope for a bounded slice, deferred with a resume point
in the ledger.

`line.sql` also doesn't exercise (out of this file's scope, tracked under
`geometry.sql`/M0134-0125 instead): the geometric operator lexer family
(`?-`, `?|`, `<->`, etc.) and line comparison/intersection functions
(`line_eq`, `line_perp`, `line_parallel`, `line_interpt`).
