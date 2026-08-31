# B-tree key encodings for int2 / oid / bool / bytea / time (M0119-0006)

Status: accepted — landed 2026-08-10.
Related: `0119-0006-array-index-key-encoding.md` (the array slice, which found
this gap while probing), `0119-0006-expression-index-result-type.md`.

## Problem

`isSupportedBTreeKeyType` (`internal/executor/operators_ddl.go`) enumerates the
column types goopg will index. Five ordinary PG types were absent:

| type | before this slice |
|---|---|
| `int2` / `smallint` | `CREATE INDEX` → `0A000 btree v0 only supports int4 / numeric keys, got "smallint"` |
| `oid` / `regproc` | same |
| `bool` / `boolean` | same |
| `bytea` | same |
| `time` (without time zone) | same |

So a `PRIMARY KEY (smallint_col)` or a plain index on a `bytea` column could not
exist at all. This is not a corner: pg_amcheck's own upstream fixtures index
`oid` columns, and `smallint` keys are routine in ported schemas. The array slice
(`0119-0006-array-index-key-encoding.md`) hit all five while probing which key
types survive `CREATE INDEX`, and recorded them for this slice.

## What the encodings must reproduce

The stored key is a byte string; the B-tree orders keys with `bytes.Compare`.
Each type's key must therefore reproduce the ordering of its **default operator
class in PG 18.3**, not the ordering of its text form. Captured from the
reference cluster (`bench/tpch/runtime`, port 65432) rather than derived by
reading the comparators:

```
int2 : -32768 -1 0 1 32767
oid  : 0 1 2147483647 2147483648 4294967295
bool : false true
bytea: (empty) 00 0001 01 ff
time : 00:00:00 00:00:00.000001 12:30:00 23:59:59.999999
```

The `oid` row is the load-bearing one. `oid` is a 4-byte type, so the obvious
key is `EncodeInt4` — and that is **wrong**: `oidcmp`
(`postgres/src/backend/utils/adt/oid.c`) compares `Oid`, an *unsigned* type, so
`2147483648` sorts above `2147483647`. The signed int4 key flips the sign bit,
which would place every OID ≥ 2³¹ *below* OID 0. goopg's codec already decodes
`oid` zero-extended into `Datum.Int` (0..2³²-1), so widening to the int8 key
gives the unsigned order for free.

The other four:

- `int2` — `btint2cmp`, signed; widened to the int4 key. Widening is
  order-preserving because every int2 value is representable as int32.
- `bool` — `btboolcmp`, `false < true`; the int4 key of 0/1.
- `bytea` — `byteacmp` (`varlena.c`): `memcmp` over the common prefix, then
  shorter-first. `btree.EncodeVarchar`'s escaped, `0x00`-terminated form has
  exactly that order **for arbitrary bytes**: it escapes `0x00`→`0x01 0x01` and
  `0x01`→`0x01 0x02`, so the terminator `0x00` is strictly below every byte that
  can appear in the body (escapes lead with `0x01`, everything else is ≥ `0x02`)
  — a prefix sorts first, and an embedded NUL cannot forge a terminator. This is
  what makes `bytea` safe as one column of a composite key.
- `time` — `bttimecmp`; int64 microseconds since midnight, through the
  timestamp/int8 key. The micros come from the codec's own `pgTimeMicros`, so
  the key derives from the same number the heap stores.

`timetz` is deliberately **declined**. `timetz_cmp_internal` compares
`time - zone` first and only then the zone itself; a single ordered key column
cannot represent a two-part comparison, and a key that claims an order it does
not have is worse than a refusal. Ledger row + `TestTimeTzIndexKeyDeclined` pin
this.

## Shape of the change

New file `internal/executor/btree_scalar_keys.go`:

- type predicates `isInt2Type` / `isOidType` / `isBoolType` / `isByteaType` /
  `isTimeOfDayType` (the last matches `time` ONLY — never `timetz`, never
  `timestamp`), added to `isSupportedBTreeKeyType`;
- `encodeScalarBTreeKey(v, col, pos) (key, handled, err)` — routed from
  `encodeBTreeKeyForColumn` before its existing type switch, so both stored-key
  writers (CREATE INDEX bulk build and the runtime maintain path) and every
  probe path share it;
- `coerceScalarKeyStringDatum` — the unknown-literal arm. A bare `WHERE b =
  '\xdeadbeef'` literal is typed `unknown` and arrives as `KindString` even
  though the stored rows decode as `KindBytes`, so the probe must coerce into
  the column's runtime kind or it would encode a different shape and silently
  find nothing. It reuses `byteaIn`, `parseTimeString` and `evalCast` rather
  than re-deriving each type's accepted input spellings;
- `decodeScalarBTreeKey(key, typeName) (datum, width, handled, err)` — the
  inverse, reporting the byte width the composite walk needs.

Both key-decode siblings route to that one decoder **before** their own
switches: `decodeIndexKeyColumn` (composite walk / amcheck) and
`decodeBTreeKeyToDatum` (single-column index-only scan). This is not just
sibling hygiene — their shared `default:` arm reads any 8 leading bytes as an
enum `float8` and never errors, so without the routing an 8-byte `oid` or `time`
key would decode as a bogus enum instead of failing (the same latent misread the
NUMERIC slice found).

## Gates

`internal/executor/btree_scalar_keys_test.go`, all five types as subtests:

- `TestEncodeScalarBTreeKeyMatchesPGOrder` — encoded keys sort in the captured
  PG order (and no key is zero-length, which `encodeIndexKeyFromCols` reads as
  "no key" and drops the row);
- `TestScalarBTreeKeyProbeMatchesStoredKey` — probe symmetry: the `KindString`
  literal encodes to the same bytes as the decoded stored datum;
- `TestScalarBTreeKeyDecodeSiblingParity` — both decode siblings invert the key,
  the composite one reports the exact width (checked against a trailing int4
  column), and the decoded datum re-encodes to the original bytes;
- `TestScalarIndexBuildAndMaintainKeys` — end-to-end over BOTH stored-key
  writers (index-then-rows vs rows-then-index), asserting physical index
  contents via `btree.RangeScan`; values are inserted in a rotated order so a
  pass cannot come from insertion order matching PG's;
- `TestByteaIndexKeyIsSelfDelimiting` — `('\x00',99)` sorts below
  `('\x0000',0)` in a composite key;
- `TestTimeTzIndexKeyDeclined` — the deliberate refusal.

Non-vacuity confirmed by disabling the encode/decode routing and dropping the
`isSupportedBTreeKeyType` additions: every test above fails (the timetz refusal
pin passes both ways by construction).

## Deferred

- `timetz` (two-part comparison) — ledger row, resume at
  `encodeScalarBTreeKey`.
- `bool`/`bytea`/`time` columns still cannot take a quoted literal on INSERT
  (`INSERT INTO t(b) VALUES ('true')` into a `bool` column raises `XX000
  expected bool, got kind 3`); that gap is in `codec.go`'s encode arms, upstream
  of index keys — ledger row.
