# `COPY` of an array column (M0119-0006, 43rd slice)

Status: accepted
Date: 2026-08-12
Task: M0119-0006 (pg_amcheck server tier cluster — array/date-time discovery line)
Closes: the 2026-08-12 deferral-ledger row filed by the 42nd slice
(`0119-0006z`), "`COPY … TO` of any NON-TEXT array column fails outright at
HEAD".

## 1. The defect

At HEAD, on a live goopg (throwaway cluster, port 5533):

```
CREATE TABLE arr(i int4[], d date[], t text[], ts timestamp[], n numeric[]);
INSERT INTO arr VALUES ('{1,2}','{2020-06-15}','{a,b}','{2020-06-15 10:00:00}','{1.50}');
SELECT i,d,t,ts,n FROM arr;   -- works: {1,2} | {2020-06-15} | {a,b} | … | {1.50}
COPY arr TO STDOUT;           -- ERROR:  expected int datum for int4, got kind 3
```

A `date[]` column gives `expected time datum for date, got kind 3`. `text[]`
alone works. The `int4[]` case carries no date-time content at all, which is
what establishes the defect as pre-existing and independent of the 42nd slice's
array/date-time work.

`COPY … FROM` had the mirror-image defect: `'{1,2}'` into an `int4[]` column
raised `invalid integer "{1,2}"`, so the codec could not read back its own
output for any non-text array column.

## 2. Root cause

A user array column is `catalog.Type{Name:<ELEMENT type>, IsArray:true}` —
`Name` holds the **element**'s name (`goopg_array_column_isarray_codec`). Both
halves of the COPY TEXT codec dispatch on `t.Name` alone:

- `datumToCopyText` (`internal/executor/copy_text.go`) — an `int4[]` column
  entered the `"int4"` arm, which requires `KindInt`, and rejected the
  `KindString` array datum the heap decode had produced (`kind 3`).
- `copyTextToDatum` — same table, inverse direction.

`text[]` escaped only by accident: its element name `text` matches no arm, so it
fell through to the `default` arm's `KindString` case, which is the right
behaviour for every array type.

Upstream has no such table. `CopyOneRowTo`
(`postgres/src/backend/commands/copyto.c`) calls the **column's** output
function, which for an array column is `array_out`; `CopyFrom`'s counterpart
calls `array_in`. goopg reaches the same place by a different route: it renders
an array to its `{…}` text at heap-decode time
(`decodeArrayValuePGStyled`, under this session's DateStyle/TimeZone — the 42nd
slice), and `encodeValuePG`'s own `IsArray` branch consumes that same text on
the way back in. So for COPY the array's output function has *already run*, and
the codec's whole job is to pass the text through.

That makes this the third place needing the identical
`IsArray`-before-the-type-switch guard, after `encodeValuePG`/
`decodePhysicalPGValueMctxStyled` (M0118-0002) and `internal/wal/pgoutput.go`'s
`pgoPhysicalAlign`/`pgoDecodePhysicalValue` (M0119-0006).

## 3. What landed

`internal/executor/copy_text.go`:

- `datumToCopyText`: `if t.IsArray` **before** the type-name switch — returns the
  rendered array text; any non-`KindString` datum is an explicit error naming
  the array type rather than a confusing element-type mismatch.
- `copyTextToDatum`: the sibling guard (Rule #2) — returns
  `NewStringDatum(raw)`, exactly what the INSERT path hands
  `encodeArrayValuePG`.

Deliberately *not* special-cased: escaping and quoting. The array text goes
through `appendCopyTextEscaped` and `appendCsvField` like any other string,
which is what reproduces upstream's output — a `bytea[]`'s backslashes get
doubled by the TEXT escaper, and `{"has,comma"}` gets the whole array CSV-quoted
with its inner quotes doubled. An array arm that did its own quoting would get
both wrong.

`internal/executor/copy_binary.go`: new `rejectBinaryCopyArray`, called at the
top of both `datumToCopyBinary` and `copyBinaryToDatum` — see §5.

## 4. Verification

**Oracle**, captured live on the PG 18.3 reference cluster (port 65432) with the
same literals, then reproduced byte-for-byte on goopg:

| probe | PG 18.3 = goopg |
|---|---|
| `COPY zz_arr TO STDOUT` | `{1,2}⇥{2020-06-15}⇥{a,b}⇥{"2020-06-15 10:00:00"}⇥{1.50}⇥{"has,comma"}` |
| `… WITH (FORMAT csv)` | `"{1,2}",{2020-06-15},"{a,b}","{""2020-06-15 10:00:00""}",{1.50},"{""has,comma""}"` |
| `SET DateStyle='German, DMY'; COPY (SELECT d, ts …) TO STDOUT` | `{15.06.2020}⇥{"15.06.2020 10:00:00"}` |

The third row matters beyond this slice: it is the 42nd slice's session-style
work arriving at COPY, and it only works because `RunCopyTo`'s scan resolves the
style — a COPY that formatted arrays itself would have printed ISO here.

`COPY … FROM STDIN` of the TEXT line round-trips on the live server
(`int4[]`/`date[]`/`text[]`/`numeric[]`, `{1.50}`'s display scale preserved).

**Gates** (`internal/executor/copy_array_test.go`, all five verified red by
scripted neutering of the two guards):

- `TestCopyTextRowEmitsArrayColumns` / `TestCopyCsvRowQuotesArrayColumns` — the
  two oracle lines above, at the row-encoder level so escaping/quoting is
  covered.
- `TestCopyTextArrayRoundTripsThroughItsOwnOutput` — `DecodeCopyTextRow`
  accepts what `EncodeCopyTextRow` wrote, per array element type.
- `TestCopyTextArrayTextReachesTheEncoder` — the round-tripped datum is one
  `encodeValuePG` actually stores (a spelling check alone would pass for a datum
  that cannot be written).
- `TestCopyBinaryRefusesArrayColumns` — the §5 refusal, plus a negative case
  proving the guard keys on `IsArray` and not on the type name.

## 5. Binary COPY: refused, loudly, on purpose

Binary COPY has the same dispatch-on-element-name shape, with two failure modes
rather than one: `int4[]` reported the confusing kind mismatch, while
`text[]`/`bytea[]` fell through to the raw-bytes arm and **silently shipped the
array's `{a,b}` text** where upstream ships `array_send`'s binary shape (ndim,
hasnull, elemtype, dims/lbounds, then per element a 4-byte length plus the
element type's own send format — `postgres/src/backend/utils/adt/arrayfuncs.c`).
A stream no real PG client can read is worse than an error, so both halves now
return `0A000 feature_not_supported` naming the type, and porting
`array_send`/`array_recv` is a ledger row with its own slice. The refusal is
pinned by a test that must be deleted when they land.

## 6. Found while verifying, NOT introduced here

`COPY … FROM` **ignores `FORMAT csv` entirely** — `copy_csv.go` has only a
write side (`EncodeCopyCsvRow`, `appendCsvField`); no CSV *reader* exists, and
grep finds no caller of one. So even an unquoted CSV line fails:

```
CREATE TABLE zz_csv(a text, b int4);
COPY zz_csv FROM STDIN WITH (FORMAT csv);
plain,7
-- ERROR:  COPY: COPY: row has 1 fields, expected 2
```

The line is being split on TAB, i.e. parsed as COPY TEXT. This is unrelated to
arrays (the reproducer is two scalar columns) and is a whole feature, not a
guard; it gets its own ledger row. A second, smaller finding rides along: after
the failure the session reports `message type 'c' not yet supported`, so the
`CopyDone` frame following a failed COPY FROM is unhandled.

## 7. Deferred (ledger rows)

1. `array_send`/`array_recv` for binary COPY (§5).
2. No CSV reader on the `COPY … FROM` path (§6).
3. The unhandled `CopyDone` frame after a failed COPY FROM (§6).
4. `COPY … FROM` of a `date[]`/`timestamp[]` array text does not honour the
   session `DateStyle` on **input**: the element encoder
   (`encodeArrayValuePG` → the element input functions) takes no style, so a
   `German, DMY` session can COPY OUT `{15.06.2020}` and not COPY it back in.
   Measured on both engines rather than inferred: under `DateStyle='German,
   DMY'`, `COPY zz_de FROM STDIN` of `{15.06.2020}` into a `date[]` column is
   `COPY 1` on PG 18.3 and `invalid input syntax for type date: "15.06.2020"`
   on goopg. The output half of that asymmetry is exactly what the 42nd slice
   fixed; the input half was never wired.
