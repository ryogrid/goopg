# 0097-0036c — TID input validation, normalization, and output (M0097)

Status: accepted

## Problem

The `tid` regress test (`postgres/src/test/regress/sql/tid.sql`) started with
~81 normalized diff lines. The leading chunk was the TID *data type* itself:

```sql
SELECT '(0,0)'::tid, '(0,1)'::tid, '(-1,0)'::tid, '(4294967295,65535)'::tid;
SELECT '(4294967296,1)'::tid;  -- error
SELECT '(1,65536)'::tid;       -- error
SELECT pg_input_is_valid('(0)', 'tid');
SELECT * FROM pg_input_error_info('(0)', 'tid');
SELECT pg_input_is_valid('(0,-1)', 'tid');
SELECT * FROM pg_input_error_info('(0,-1)', 'tid');
```

goopg's `::tid` cast was a **no-op string passthrough** in `evalCast`
(`internal/executor/expr.go`): the value fell through the big type switch to
`return d, nil // pass-through for unknown types`. Consequences:

- `'(-1,0)'::tid` printed `(-1,0)` instead of PG's `(4294967295,0)` — the block
  number is an unsigned 32-bit `BlockNumber`, not a signed int.
- `'(4294967296,1)'::tid` (block > uint32 max) and `'(1,65536)'::tid`
  (offset > uint16 max) were silently accepted instead of raising
  `invalid input syntax for type tid`.
- `pg_input_is_valid('(0)', 'tid')` returned `t` (the function's default for
  unknown types) instead of `f`; `pg_input_error_info` returned 0 rows for the
  same malformed inputs instead of the error row.

## PostgreSQL semantics (the oracle)

`tidin` (`postgres/src/backend/utils/adt/tid.c`) parses `(block,offset)`:

- It scans for `(`, the first `,` (DELIM), and stops at `)` (RDELIM), filling
  exactly two coordinate strings; fewer than two → error (so `(0)` is invalid).
- **block**: `strtoul(coord[0], &badp, 10)` — accepts an optional sign, base-10
  digits, and requires `*badp == DELIM` (no trailing junk before the comma).
  With `SIZEOF_LONG > 4` (64-bit), it then guards against values outside
  `BlockNumber` range with a round-trip check that accepts a value iff it equals
  either its `uint32` truncation **or** its sign-extended `int32` truncation.
  This is exactly why `-1` is accepted (→ `4294967295`) while `4294967296`
  (= 2^32) is rejected.
- **offset**: `strtoul(coord[1], &badp, 10)` with `*badp == RDELIM` and
  `cvt <= USHRT_MAX` (65535). So `65536` and `-1` (which wraps huge) are
  rejected. Text after `)` is never examined.
- Output (`tidout`) is `(%u,%u)` — unsigned.

## Fix

`internal/executor/expr.go`:

- New `cStrtoul10Full(s)` helper — emulates C `strtoul(s,&end,10)` plus PG's
  "fully consumed up to the delimiter" requirement: skips leading C whitespace,
  accepts `+`/`-`, base-10 digits to end-of-string, wraps negatives modulo 2^64,
  and reports overflow past 64 bits.
- New `parseTidInput(str) (block uint32, offset uint16, ok bool)` — locates
  `(`, the first `,`, and the closing `)`; applies `cStrtoul10Full` plus the
  block round-trip guard (`bcvt == uint64(block) || bcvt == uint64(int64(int32(block)))`)
  and the `offset <= 65535` bound. Trailing text after `)` is ignored, matching
  `tidin`'s scan loop.
- `evalCast` gains a `case "tid":` that, for a `KindString` input, validates via
  `parseTidInput` and re-emits the canonical `fmt.Sprintf("(%d,%d)", block, offset)`
  (block is `uint32`, so `%d` prints the unsigned value). Invalid input →
  `*ExecError{Code: "22P02", Message: "invalid input syntax for type tid: %q"}`.
- `pg_input_is_valid` gains a `case "tid":` returning `parseTidInput`'s `ok`.

`internal/executor/operators_pg_input_error_info.go`:

- `pg_input_error_info` gains a `case "tid":` that reports
  `invalid input syntax for type tid: "<v>"` / `22P02` on failure (0 rows when
  valid), mirroring the sibling validator.

The two validation entry points (`pg_input_is_valid` and `pg_input_error_info`)
and the cast all route through the single `parseTidInput` helper, keeping the
sibling paths in sync per the repo's recurring "sibling code paths must agree"
rule.

## Tests

- `internal/executor/tid_cast_test.go`: `TestParseTidInput` (block wraparound,
  range guards, offset bound, trailing-junk rejection, trailing-text-after-`)`
  acceptance) and `TestEvalCastTidNormalizesAndValidates` (canonical
  normalization + 22P02 on out-of-range block).
- End-to-end: `tid` regress normalized diff **81 → 47 lines** (verified via
  `GOOPG_REGRESS_DIFF_DIR`). Full `internal/executor` package green except the
  pre-existing, unrelated `TestAnalyzeRespectsStatsTarget` failure (ANALYZE
  sampling NDistinct=398 want 400, documented in fix_plan.md).

## Out of scope (residual `tid` diff, 47 lines)

All remaining `tid` diff is TID *handling functions and system columns*, not the
type's I/O:

- `min(ctid)` / `max(ctid)` aggregates over real heap ctids.
- `currtid2()` — PG-internal "latest visible tid for a relation", with its
  relkind-specific errors (indexes, views with no CTID, matviews/tables).
- `ctid` system-column access on views/indexes/sequences and the associated
  `column "ctid" does not exist` vs PG's specific error wording.

These are separate features (heap/relcache integration + a builtin function) and
are tracked under M0097-0036 as remaining work.
