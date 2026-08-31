# M0134-0149 — `path.sql` path_in-faithful parsing (PARKED)

**Date:** 2026-08-25
**Status:** contained fix landed, PARKED — matches the M0134-0094 (`box.sql`)
/ M0134-0098 (`circle.sql`) / M0134-0136 (`line.sql`) / M0134-0137 (`lseg.sql`)
precedent exactly.

## Result

`scripts/pg-regress-runner.sh path` against PG 18.3: diff went from
**111 lines (0% parity)** to **31 lines**, with **zero residual
`^ERROR`/`^-ERROR` mismatches** — every remaining diff line is the already-
ledgered psql LINE-position-echo cross-cutting gap shared with box.sql/
circle.sql/line.sql/lseg.sql (see "Not fixed this loop" below).

## Root cause

`path` was a raw-varlena pass-through with no validation at all, the same
state `box`/`circle`/`line`/`lseg` were in before their respective M0134
slices landed: any string, well-formed or not, was stored and echoed back
verbatim (no reordering of points, no open/closed normalization). The
`isopen`/`isclosed`/`pclose`/`popen` functions had no handler despite
`pg_proc` already seeding all four OIDs (1430/1431/1433/1434) —
`function isopen does not exist` etc. — and `pg_input_is_valid`/
`pg_input_error_info` had no `path` case, so both silently answered "valid"
for malformed path text.

## What landed

1. **`parsePathLiteral`** (`internal/executor/expr.go`) — a faithful port of
   `path_in`/`path_decode`/`pair_count`
   (`postgres/src/backend/utils/adt/geo_ops.c`):
   - the point count is pre-computed from the **total comma count** exactly
     as `pair_count` does — an even (including zero) count is rejected
     before any point parsing is attempted, which is why `'[]'` (zero
     commas) and other malformed inputs fail immediately rather than during
     point parsing;
   - a single leading `'('` with **no other `'('` anywhere else in the
     string** (the "quick entry" wrapped form, e.g. `'(1,2,3,4)'`) is
     stripped by `path_in` itself, tracked as a separate `outerDepth` from
     `path_decode`'s own wrapper-depth;
   - `path_decode` then recognizes `'['` (open path), a doubled `'(('`
     (closed path with an extra wrapper), or no wrapper at all (bare
     `"1,2,3,4"` or singly-wrapped `"(1,2),(3,4)"`);
   - reuses `linePairDecode`/`lineSingleDecode` (already shared by
     `parseLineLiteral`/`parseLsegLiteral` via `pathDecodeTwoPoints`) for the
     per-point float8 decode, since `path_decode`'s coordinate-pair grammar
     is identical regardless of point count.
   - **`pathCanonicalText`** — mirrors `path_out`'s `path_encode`:
     `"[(x,y),...]"` for an open path, `"((x,y),...)"` for a closed one.

2. **Wiring**, mirroring the box/circle/line/lseg chokepoint pattern exactly:
   - `internal/executor/codec.go` `coerceTextLikeDatum` — a `path` column's
     assignment coercion now validates + canonicalizes via
     `parsePathLiteral`/`pathCanonicalText`.
   - `pg_input_is_valid`/`pg_input_error_info` (`expr.go` +
     `operators_pg_input_error_info.go`) — new `"path"` cases, the latter
     surfacing `parsePathLiteral`'s exact message/SQLSTATE so
     `pg_input_error_info('[(1,2),(3)]', 'path')` matches PG's DETAIL row
     byte-for-byte (verified live: `path.sql`'s two `pg_input_error_info`
     calls now match exactly, including the zero-length message/detail/hint
     columns PG's `errcode`-only `ereturn` produces).

3. **`isopen`/`isclosed`/`pclose`/`popen`** (`evalFuncCall`, `expr.go`) — thin
   wrappers around `parsePathLiteral`'s `closed` flag (`isopen`/`isclosed`,
   `path_isopen`/`path_isclosed` in `geo_ops.c` — no re-parsing or
   point-count logic of their own beyond reading the stored flag) and
   `pathCanonicalText` with the flag forced (`pclose`/`popen`,
   `path_close`/`path_open` — points and point count unchanged, only the
   open/closed flag and the corresponding `[`/`(` `]`/`)` delimiters differ).

## Not fixed this loop

The residual 31-line diff is a single already-known, already-ledgered
cross-cutting gap (M0134-0094/-0098/-0136/-0137's ledger rows, standing
recommendation item #19 in `.ralph/working_set.md`'s carried list): PG's
parse-analysis-time literal-cast errors carry a wire-protocol error
`Position`, which psql's client renders as a `"LINE N: ...\n  ^"` echo;
`coerceTextLikeDatum` never attaches `ExecError.Pos`, so goopg's error
responses omit it. Fixing this touches every `coerceTextLikeDatum` caller
across the INSERT/UPDATE/COPY pipeline — out of scope for a bounded slice,
already tracked, no new ledger row needed (this loop hit the exact same gap
the prior four geometry slices already recorded).

`path.sql` also doesn't exercise (out of this file's scope, tracked under
`geometry.sql`/M0134-0125 instead): the geometric operator lexer family
(`?-`, `?|`, `<->`, `##`, etc.) and path comparison/length functions.
