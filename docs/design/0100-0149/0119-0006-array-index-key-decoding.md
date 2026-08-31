# M0119-0006 — ARRAY B-tree key DECODING (the sibling the encode slice left open)

**Status:** implemented 2026-08-10 · **Milestone:** M0119-0006 (pg_amcheck server
tier) · **Predecessor:** `0119-0006-array-index-key-encoding.md`

## Problem

The array ENCODE slice (2026-08-10) taught `encodeBTreeKeyForColumn` to write an
`array_ops`-ordered key for an array key column:

```
non-NULL element:  0x01 ++ <element's own scalar key bytes>
NULL element:      0xFF
end of array:      0x00
```

It did **not** teach the decode side to read it. That was not merely a missing
feature — it left a live misread, because both decode siblings dispatch on
`col.Type.Name`, and for an array column `catalog.Type` holds the **ELEMENT**
type name in `Name` with `IsArray` carrying the array-ness (`codec_array.go`).
So every `Name`-only predicate answered for the element and claimed the array:

- an `int4[]` key column reached `decodeIndexKeyColumn`'s `isInt4Type` arm, and
- an `int2[]` / `oid[]` / `bool[]` one reached `decodeScalarBTreeKey`'s arm,

each consuming the ELEMENT's fixed width out of a longer array segment — for
`int4[]` the four bytes `0x01` (presence tag) plus the first three bytes of the
first element's key — and returning that as the column's value.

Two silent consequences, in increasing severity:

1. **A single-column index-only scan over an array column produced a garbage
   integer** instead of the array (`decodeBTreeKeyToDatum`, reached from
   `indexOnlyScanOp.decodeRowFromKey`).
2. **A composite key walk desynchronized at the array column**, so every *later*
   key column decoded from the wrong offset. That walk is
   `btIndexOpClassComparator`'s — the amcheck comparator this whole M0119-0006
   cluster exists to make faithful. Verified at HEAD before the fix: a composite
   index `(a int4[], i int4 int4_arrtail_ops)` whose user `FUNCTION 1` support
   proc had been repointed at a *descending* comparator was reported **clean** by
   `bt_index_check`, because the routine was handed the tail of the array's own
   bytes instead of column `i`.

## Fix

`decodeArrayBTreeKey` (`internal/executor/btree_array_key.go`) inverts the
encoding: read a tag, dispatch on it, recurse into `decodeIndexKeyColumn` with
the ELEMENT column for a present element, stop at the end marker, and report the
byte width consumed — which is what makes an array usable as one column of a
composite walk. It returns the array's canonical text form (`"{1,2}"`) as the
`KindString` datum every other goopg path carries an array value in, the same
representation the heap-side decode produces (`decodeArrayValuePG`).

Routing: **arrays go first** in both siblings, before `decodeScalarBTreeKey` and
before their own type switches — the exact mirror of
`encodeBTreeKeyForColumn`'s array-first routing, and for the same reason (no
`Name`-only predicate may see an array). `decodeBTreeKeyToDatum` additionally
requires the array segment to consume the **whole** key: a single-column key *is*
the one column, so trailing bytes mean this is not the encoding we think it is,
and the other single-column arms are equally strict (`btree.DecodeInt4` enforces
an exact length).

Element rendering (`decodeArrayKeyElemText`) is per-type, and deliberately so:
the element *value* comes from the one decoder the encoder's own recursion pairs
with, but a `Datum` alone does not say how PG spells it. A float has to go
through `PGFloatOut` (the decoder hands back Go's `'g'` form — round-trip exact,
but not PG's spelling) and a text-like element has to be re-quoted by
`array_out`'s rules (`quoteArrayTextElem`, shared with the heap path). The
covered set matches what the heap array codec can actually store
(`arrayElemTypeInfo`): int2/int4/int8/oid/bool/float4/float8 plus varlena
text-likes, plus numeric which the key path can encode. Anything else is
**refused, not guessed** — an error, which is also what re-arms the comparator's
decode-failure fallback (the NUMERIC slice found the shared `default:` arm never
erroring, which had disabled that fallback).

The decode is value-preserving, not byte-preserving, exactly as the scalar
decodes are: `EncodeNumericKey` strips trailing mantissa zeros, so an element
`1.50` returns as `1.5`.

## Gates (all confirmed non-vacuous — routing disabled ⇒ each fails)

| test | property |
|---|---|
| `TestDecodeArrayBTreeKeyRoundTrip` | canonical text per element type (int2/int4/int8/oid/bool/float8/numeric/text), incl. `{}`, NULL elements, int bounds, a quoted element |
| `TestArrayBTreeKeyDecodeSiblingParity` | the two decode siblings agree (Hard-won Rule #2) |
| `TestDecodeArrayBTreeKeyCompositeWalk` | the array column consumes exactly its segment, so the NEXT column decodes correctly |
| `TestDecodeArrayBTreeKeyRejectsMalformed` | unterminated / bad tag / truncated / trailing-byte keys error instead of mis-reading |
| `TestBtIndexCheck_OpClassDamageDetectedAfterArrayColumn` | end-to-end: damage after an array key column is now reported (pre-fix: **reported clean**) |

## Deferred (ledger rows 2026-08-10)

- **A quoted `"NULL"` text element is encoded as a NULL element.**
  `encodeArrayBTreeKey` tests the parsed element against `NULL` after
  `parseTextArray` has already stripped the quoting, so `'{"NULL"}'::text[]` and
  `'{NULL}'::text[]` get the same key. Upstream `array_in` treats only the
  *unquoted* token as NULL. Fixing it needs a quote-aware element parse (the
  parser must report per element whether it was quoted).
- **Element types with no key decoding**: bytea/date/time/timestamp/enum arrays.
  Not reachable from a stored array today (the heap array codec cannot write
  them), so the decode refuses them rather than inventing a rendering.
- Multidimensional arrays / non-default lower bounds remain declined at the
  encode side (pre-existing row).
