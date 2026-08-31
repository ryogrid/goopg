# M0134-0137 — `lseg.sql` lseg_in-faithful parsing (PARKED)

**Date:** 2026-08-24
**Status:** contained fix landed, PARKED — matches the M0134-0094 (`box.sql`)
/ M0134-0098 (`circle.sql`) / M0134-0136 (`line.sql`) precedent exactly.

## Result

`scripts/pg-regress-runner.sh lseg` against PG 18.3: diff went from
**67 lines (0% parity)** to **27 lines**, with **zero residual
`^ERROR`/`^-ERROR` mismatches** — every remaining diff line is the already-
ledgered psql LINE-position-echo cross-cutting gap shared with box.sql/
circle.sql/line.sql (see "Not fixed this loop" below).

## Root cause

`lseg` was a raw-varlena pass-through with no validation at all — the same
state `line` was in before M0134-0136 landed `parseLineLiteral`. Any string,
well-formed or not, was stored and echoed back verbatim; the
`lseg(point, point)` two-point constructor function had no handler at all
(`function lseg does not exist`, despite `pg_proc` already seeding the OID);
and `pg_input_is_valid`/`pg_input_error_info` had no `lseg` case.

## What landed

1. **`pathDecodeTwoPoints`** (`internal/executor/expr.go`) — extracted from
   `parseLineLiteral`'s existing two-point-form branch (path_decode's
   `opentype=true, npts=2` grammar: an optional `[` or (single/doubled) `(`
   wrapper, then two coordinate pairs). Both `parseLineLiteral` and the new
   `parseLsegLiteral` now call this one shared implementation — `lseg_in`
   (`postgres/src/backend/utils/adt/geo_ops.c:2065-2077`) delegates to the
   exact same `path_decode` PG uses for line's two-point form.

2. **`parseLsegLiteral`** (`internal/executor/expr.go`) — a faithful port of
   `lseg_in`. Two behavioral differences from `line_in`'s two-point form,
   both confirmed against `geo_ops.c`:
   - **no `{A,B,C}` coefficient form** — lseg has no analog, only the
     two-point grammar.
   - **no distinct-points check** — `lseg_in` stores the two points directly
     via `statlseg_construct` with zero validation, so a degenerate segment
     like `'[(1,1),(1,1)]'` is valid PG input (unlike `line_in`'s two-point
     form, which rejects coincident points with "must be two distinct
     points").

3. **Wiring**, mirroring the box/circle/line chokepoint pattern exactly:
   - `internal/executor/codec.go` `coerceTextLikeDatum` — an `lseg` column's
     assignment coercion now validates + canonicalizes via
     `parseLsegLiteral`/`lsegCanonicalText` (`"[(x1,y1),(x2,y2)]"`, matching
     `lseg_out`'s `path_encode(PATH_OPEN, 2, ...)`).
   - `pg_input_is_valid`/`pg_input_error_info` (`expr.go` +
     `operators_pg_input_error_info.go`) — new `"lseg"` cases, the latter
     surfacing `parseLsegLiteral`'s exact message/SQLSTATE so
     `pg_input_error_info('[(1,2),(3)]', 'lseg')` matches PG's DETAIL row
     byte-for-byte.
   - `lseg.sql` does not exercise `lseg '...'` typed-literal cast syntax (no
     `evalTypedStringLit` arm was needed this loop, unlike box/circle/line);
     `point`/`line` were already added to the parser's typed-literal
     whitelist by M0134-0136, `lseg` was not added since nothing in this
     file requires it.

4. **`lseg(point, point)` constructor** (`evalFuncCall`, `expr.go`) — PG's
   `lseg_construct`/`statlseg_construct`: copies the two points directly with
   no validation, unlike `line(point,point)`'s `line_construct_pp` (which
   does check for distinct points).

## Not fixed this loop

The residual 27-line diff is a single already-known, already-ledgered
cross-cutting gap (M0134-0094/-0098/-0136's ledger rows): PG's
parse-analysis-time literal-cast errors carry a wire-protocol error
`Position`, which psql's client renders as a `"LINE N: ...\n  ^"` echo;
`coerceTextLikeDatum` never attaches `ExecError.Pos`, so goopg's error
responses omit it. Fixing this touches every `coerceTextLikeDatum` caller
across the INSERT/UPDATE/COPY pipeline — out of scope for a bounded slice,
deferred with a resume point in the ledger.

`lseg.sql` also doesn't exercise (out of this file's scope, tracked under
`geometry.sql`/M0134-0125 instead): the geometric operator lexer family
(`?-`, `?|`, `<->`, `##`, etc.) and lseg comparison/intersection functions
(`lseg_eq`, `lseg_perp`, `lseg_parallel`, `lseg_intersect`, `lseg_interpt`,
`lseg_center`).
