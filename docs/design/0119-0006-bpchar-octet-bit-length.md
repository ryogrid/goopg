# M0119-0006 (65th slice) — `octet_length()`/`bit_length()` on a `bpchar` answer from the declared width, not the trimmed heap image

Closes the deferral the 57th slice filed (ledger row 1314). The four render
boundaries the 57th slice closed (`appendTypedCellText`, `datumToCopyText`,
`datumToCopyBinary`, `pgoDecodePhysicalValue`) all already held the column's
`catalog.Type`, so each could re-pad via the shared `catalog.PadBpchar` for
free. The two remaining defect sites are **expression evaluations** — the
argument's declared type is not threaded to the function, so `octet_length()`
and `bit_length()` on a `bpchar` column answered from the trimmed heap image
(`2` for a `char(10)` holding `'ab'` where PG 18.3 says `10`).

## The defect

goopg stores `bpchar` TRIMMED (deliberate, load-bearing since M0103-0007 —
see the sibling `docs/design/0119-0006-bpchar-declared-width.md`); the declared
width lives only in the column's `catalog.Type.Args[0]` typmod. On the render
boundaries that type is in hand, so padding was recovered on the way out. In
`evalFuncCall`, `octet_length`/`bit_length` received only the evaluated
`Datum` — a plain `KindString` with the trailing blanks already trimmed away —
so `len()` answered from the trimmed image:

- `SELECT octet_length('ab'::char(10))` → `2` where PG 18.3 says `10`.
- `bit_length` had no `evalFuncCall` case at all; it fell through to the
  `evalStoredRoutineFuncCall` fallback and raised `function does not exist`
  for a plain `char`/`bpchar` argument.

The `length()` function escapes the defect only by luck: upstream `bpcharlen`
uses `bcTruelen` (`varchar.c`), which trims trailing spaces — so the trimmed
answer is the correct one.

## What PG actually does

Both functions are builtins in `evalFuncCall`'s namespace, so the correct
behaviour must be reconstructed from upstream source (there is no OID-based
dispatch; goopg resolves builtins by name — see `exprType`/`evalFuncCall`).

### `octet_length` — the padded image

Upstream `bpcharoctetlen` (`postgres/src/backend/utils/adt/varchar.c:709`):

```c
Datum
bpcharoctetlen(PG_FUNCTION_ARGS)
{
    Datum        bpchar = PG_GETARG_DATUM(0);
    int            result;

    result = toast_raw_datum_size(bpchar) - VARHDRSZ;
    PG_RETURN_INT32(result);
}
```

`toast_raw_datum_size` measures the STORED image — which, for `bpchar`, is
blank-padded to the declared width. So `octet_length('ab'::char(10))` = **10**
(bytes of the padded value, including the 8 trailing spaces), and for a
multibyte value `'あ'::char(5)` = **7** (padded to 5 runes = 2+2+1+1+1 bytes).

`octet_length` on an *unbounded* `bpchar` (typmod −1, the spelling the 58th
slice made verbatim) measures the value as stored — trailing blanks kept — so
`octet_length('ab  '::bpchar)` = 4. `PadBpchar` with no width is identity, so
the same helper serves both.

### `bit_length` — the trimmed image, via the implicit cast

`bit_length` is *not* a per-type `bit_length` builtin for `bpchar`. It is the
SQL-standard `octet_length($1) * 8` family (OIDs 1810 bytea / 1811 text / 1812
bit). For a `bpchar` argument, PG's implicit **bpchar→text** cast (`char`,
`bpchartotext` in `varchar.c`) applies first — and `bpchartotext` trims
trailing blanks (`text_to_cstring` on the `bcTruelen`-trimmed value). So:

```sql
SELECT bit_length('ab'::char(10));  -- 16, NOT 80
```

The 8 trailing spaces are trimmed by the implicit cast before `octet_length`
runs, so `bit_length` measures the TRIMMED byte length × 8. This is the
opposite of `octet_length` — and the deferred row's "2 where PG says 10 for
both" was wrong for `bit_length`: a `char(10)` holding `'ab'` gives **16**, not
80. The two functions are not symmetric, and the fix mirrors that asymmetry.

(goopg does not parse `B'101'` bit literals, so the `bit`-argument form of
`bit_length` (OID 1812) is unreachable; `bit_length` is implemented for
`KindBytes` (bytea: `8 * len`) and `KindString`.)

### bare `char` / `character`

`char`/`character` without a length is `char(1)` — the parser's
`synthesizeBareCharTypmod` synthesises typmod 1 for a bare cast, and a
declared-width probe that finds no typmod normalises to 1. So
`octet_length(''::char)` = 1 (one padded space) and `bit_length(''::char)` = 0
(trimmed to empty × 8). Both verified on the oracle.

## The fix

Two pieces in `internal/executor/expr.go`, before/inside `evalFuncCall`:

1. **`declaredBpcharTypmod(e planner.Expr) int64`** — walks the argument
   expression to recover the declared `bpchar` width when the planner carries
   one:
   - `*planner.ColumnRef` → `n.Type.Args[0]` when `n.Type.Name` is
     `char`/`bpchar`/`character` and the column is not an array.
   - `*planner.CastExpr` → `n.Typmod` for a `::char(n)`/`::bpchar(n)` cast.
   - `coalesce`/`greatest`/`least`/`nullif` → recurse to the first argument
     (PG resolves these functions' result type from the first argument's).
   - everything else → 0 (no declared width in hand).
   A missing/`<=0` typmod normalises to 1 (bare `char`).
   The `IsArray` guard matters: `int4[]` is `Type{Name:"int4",IsArray:true}`
   (the `goopg_array_column_isarray_codec` gotcha), so a non-array check keeps
   an array-of-char column from padding each element as a scalar `char`.

2. **`octet_length` case** gains the non-string `42883` guard the sibling
   `length` case already has, then — when the declared typmod is recoverable —
   answers `len(catalog.PadBpchar(t, s))` for `t = catalog.Type{Name:"char",
   Args:[typmod]}`; otherwise falls back to the plain trimmed `len`. (The
   `PadBpchar` typmod path IS the padded answer; the fallback matches the
   unbounded-`bpchar` and plain-text cases, where the stored image is the
   answer.)

3. **`bit_length` case** (new): `KindBytes` → `8 * len(BytesValue())`;
   `KindString` → `8 * len(StringValue())` (the implicit trimming cast means
   the trimmed image is the correct answer); anything else → `42883`.

### Error text parity

The `42883` guard mirrors the `length` sibling exactly — `function
octet_length(integer) does not exist` (PG's `funcname_signature_string` shape
with the arg's SQL type name from `stringFuncArgTypeName`). Verified
byte-for-byte against PG 18.3 for `octet_length(5)` and `bit_length(5)`.

## New / changed symbols

- `internal/executor/expr.go`
  - `stringFuncArgTypeName(k DatumKind) string` — SQL type name for the
    `42883` message (shared with the `length` sibling; `KindInt`→integer,
    `KindNumeric`→numeric, `KindBool`→boolean, `KindTime`→timestamp,
    else unknown).
  - `declaredBpcharTypmod(e planner.Expr) int64` — the declared-width probe.
  - `evalFuncCall`'s `octet_length` case — 42883 guard + `PadBpchar` arm.
  - `evalFuncCall`'s `bit_length` case — new, `KindBytes`/`KindString`.

## Tests

`internal/executor/bpchar_declared_width_test.go` →
`TestOctetBitLengthRespectBpcharDeclaredWidth` (19 subtests via the
`newDDLFixture`/`byteaExprResult` harness):

- plain text: `octet_length('abc')`=3, `bit_length('abc')`=24
- bytea: `bit_length('\x01020304'::bytea)`=32, `octet_length('\xaabb'::bytea)`=2
- declared width: `octet_length('ab'::char(10))`=10, `octet_length(''::char(10))`=10,
  `octet_length('ab'::char(1))`=1, `octet_length('あ'::char(5))`=7
- bare char: `octet_length(''::char)`=1
- bit_length asymmetry: `bit_length('ab'::char(10))`=16, `bit_length('ab'::char(1))`=8,
  `bit_length(''::char)`=0, `bit_length('あ'::char(5))`=24
- explicit text cast overrides: `octet_length('ab'::char(10)::text)`=2,
  `bit_length('ab'::char(10)::text)`=16
- column source (values-list `char(10)` column), coalesce-threaded width
- 42883 guards: `octet_length(5)`, `bit_length(5)`

All 19 match a PG 18.3 oracle (probed live on a throwaway goopg, port 5533,
against the reference at `./postgres/`).

## Gates

- `go build ./internal/...` clean.
- `go test ./internal/executor/` — all PASS except the **pre-existing foreign
  failure** `TestRegIdentifierInputResolvesRegtypeName` (untracked
  reg_identifier WIP from a prior loop; not caused by, and independent of,
  this change — see the ledger row's deferred column for the diagnosis).
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — every
  package PASS except that same foreign test (the only failure in the run).
- Commit-hook pgbench smoke runs on the commit (foreign-WIP-free HEAD).

1 ledger row resolved (1314). Design `0119-0006-bpchar-octet-bit-length.md` +
README row.
