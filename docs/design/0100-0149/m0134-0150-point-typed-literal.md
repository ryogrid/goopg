# M0134-0150 — `point.sql` point_in-faithful parsing (PARKED)

**Date:** 2026-08-25
**Status:** contained fix landed, PARKED — matches the M0134-0094 (`box.sql`)
/ M0134-0098 (`circle.sql`) / M0134-0136 (`line.sql`) / M0134-0137 (`lseg.sql`)
/ M0134-0149 (`path.sql`) precedent, with two additional sibling-path fixes
this loop's audit surfaced.

## Result

`scripts/pg-regress-runner.sh point` against PG 18.3: diff went from
**531 lines (0% parity)** to **451 lines**. Unlike the prior five geometry
slices, this one does NOT reach zero residual `^ERROR`/`^-ERROR` mismatches
— the INSERT-validation and column-storage portion of the diff is fully
fixed, but a large, separate operator-lexer gap dominates the remainder (see
"Not fixed this loop").

## Root cause

`point` was a raw-varlena pass-through with **zero validation at all** — the
same state box/circle/line/lseg/path were each in before their own M0134
slices, but point itself had never been individually audited (`.ralph/
working_set.md`'s standing recommendation item #5 flagged it as
unaudited going into this loop). Garbage input like `'asdfasdf'` or
`'(10.0 10.0)'` was stored and echoed back verbatim; `pg_input_is_valid`
answered `t` for that same garbage.

## What landed

1. **`parsePointLiteral`/`pointCanonicalText`** (`internal/executor/expr.go`)
   — a faithful port of `point_in`/`point_out`
   (`postgres/src/backend/utils/adt/geo_ops.c`):
   - `point_in` is a single `pair_decode` call with `endptr_p == NULL`,
     i.e. the **entire** input string must be consumed by the one
     `(x,y)` pair — no trailing text tolerated at all. This is the one
     place point's grammar is *stricter* than path/line/lseg's own
     `pair_decode`/`path_decode` callers, which each consume one pair out
     of a larger multi-point string and hand the remainder back to their
     own wrapper-delimiter logic. `parsePointLiteral` reuses the existing
     shared `linePairDecode` primitive (already used by path/line/lseg) and
     adds point's own trailing-text check on top.
   - `pointCanonicalText` mirrors `point_out`'s `path_encode(PATH_NONE, 1,
     pt)` — a bare `"(x,y)"` pair with no outer wrapper delimiter (unlike
     path's `[...]`/`(...)` wrapper).

2. **Wiring**, mirroring the box/circle/line/lseg/path chokepoint pattern:
   - `internal/executor/codec.go` `coerceTextLikeDatum` — new `"point"`
     case.
   - `pg_input_is_valid`/`pg_input_error_info` (`expr.go` +
     `operators_pg_input_error_info.go`) — new `"point"` cases.
   - `evalCast`'s `::point` arm (`expr.go`) — **newly added**, was entirely
     missing (fell through to the bottom default arm, returning the input
     datum unchanged — same sibling-gap shape M0134-0087 found for
     `xid`/`xid8`: the `TypedStringLit` spelling validates, the `CastExpr`
     spelling silently doesn't).
   - `evalTypedStringLit`'s `point '...'` arm (`expr.go`) — **newly added**,
     was also entirely missing.

3. **Second sibling-path bug found via this loop's own audit**: while wiring
   point's `::point`/`point '...'` arms, the same two arms were checked for
   `lseg`/`path` and found **also missing** despite both already having real
   parsers (`parseLsegLiteral`/M0134-0137, `parsePathLiteral`/M0134-0149) —
   `'...'::lseg`, `'...'::path`, `lseg '...'`, and `path '...'` were all
   silent unvalidated pass-throughs. Added all four arms in the same loop
   (`evalCast`'s `lseg`/`path` cases, `evalTypedStringLit`'s `lseg`/`path`
   cases), reusing the existing parsers — no new parsing logic needed, only
   wiring. Confirmed no regression on lseg.sql(27)/path.sql(31) (both
   unchanged).

4. **Parser typed-literal whitelist gap**: `internal/parser/select.go`'s
   `tryTypedLiteral` had `point`/`line`/`box`/`circle` but was missing
   `lseg`/`path` — so a bare `path '...'`/`lseg '...'` in an *expression*
   context (not a column-assignment context) parsed as two unrelated tokens
   instead of one typed literal. This is exactly what point.sql's `p.f1 <@
   path '[(0,0),(-10,0),(-10,10)]'` needs. Added both to the whitelist.

## Not fixed this loop

The residual 451-line diff has three distinct causes, none of which is a
`point`-specific gap in the sense the prior five geometry slices closed:

1. **Geometric operator lexer family** (dominant cause) — `|>>` (above),
   `<<|` (below), `~=` (same-as), `<->` (distance) have no parser token at
   all. This is the SAME standing gap already recorded against line.sql/
   lseg.sql's M0134-0136/-0137 ledger rows and geometry.sql's M0134-0125
   sizing (`?-`, `?|`, `?#`, `@@`, `#`, `&<`, `&>` also still missing) —
   reconfirmed here, not a new discovery.

2. **`point <@ path` operator dispatch** — narrower, new: `<@`/`@>` are
   currently only implemented for the point-in-box case. This loop's
   parser-whitelist fix makes `p.f1 <@ path '...'` *parse* correctly for
   the first time, but it now fails at evaluation with `operator <@:
   invalid box value` instead of running PG's `path_contain_pt`/`on_ps`
   point-in-polygon check.

3. **Test-harness ordering artifact, NOT a goopg bug** — investigated and
   ruled out. `scripts/pg-regress-runner.sh`'s setup phase unconditionally
   runs `create_index.sql` as a first-group prerequisite (for
   `aggregates.sql`'s benefit), and `create_index.sql:85` does `INSERT INTO
   POINT_TBL(f1) VALUES (NULL)`. Real PG's `parallel_schedule` runs the
   `point` test group *before* the `create_index` group, so upstream's own
   `point.out` never accounts for that NULL row — but the runner's
   single-target invocation always runs `create_index.sql` first regardless
   of which named test was requested, so `SELECT * FROM POINT_TBL` shows
   `(11 rows)` instead of PG's expected `(10 rows)`. Reproduced and
   confirmed as harness-only via a standalone manual run (`test_setup.sql` +
   `point.sql` only, no `create_index.sql`), which reproduced PG's exact
   10-row output byte-for-byte. See the M0134-0150 deferral-ledger row for
   the optional schedule-group-awareness fix (same underlying gap
   M0134-0125's geometry.sql sizing already flagged).

## Resume points

- Operator lexer: `.ralph/deferral_ledger.md`'s M0134-0136/-0137/-0150 rows.
- `point <@ path`: extend the `<@`/`@>` dispatch site (near
  `parseBoxLiteral`'s call sites in `internal/executor/expr.go`) with a
  path-target arm calling a new `pathContainsPoint` port of
  `path_contain_pt`/`on_ps` (`geo_ops.c`).
- Harness ordering: `scripts/pg-regress-runner.sh`'s prerequisite block,
  make it schedule-group-aware (lower priority — doesn't affect point.sql's
  own scored diff, only the noisy row-count line).
