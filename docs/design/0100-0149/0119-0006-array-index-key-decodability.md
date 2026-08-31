# M0119-0006 — array index-key DECODABILITY: an indexed `interval[]` column made a plain SELECT fail

Status: accepted (landed 2026-08-12)

Related: `0119-0006-array-index-key-encoding.md` (the encoder),
`0119-0006-array-index-key-decoding.md` (the decoder),
`0119-0006-interval-index-key-encoding.md` (the first non-invertible key, and
the `indexKeyIsDecodable` seam this slice generalises).

## The defect

goopg's blob B-tree key is one order-preserving byte run per key column, and an
index-only scan over an ALL_VISIBLE page answers the query **from the key** —
`indexOnlyScanOp.decodeRowFromKey`. A few key encodings are deliberately not
invertible, so that fast path has to be declined for them; the predicate that
declines it read

```go
if !col.Type.IsArray && isIntervalTypeName(col.Type.Name) {
    return false
}
```

The `!col.Type.IsArray` guard is the bug, and it is array-shaped on purpose: a
goopg array column carries `catalog.Type{Name:<ELEMENT>, IsArray:true}`, so
without the guard `interval[]` would have matched the interval predicate for the
wrong reason (the name is the ELEMENT's). The guard fixed the reason and
inverted the answer — an `interval[]` column, whose element key is the *same*
non-invertible `interval_cmp_value` span, was reported DECODABLE.

Confirmed at HEAD, no corruption and no exotic plan required:

```
CREATE TABLE av (i interval[]);
CREATE INDEX av_idx ON av (i);
INSERT INTO av VALUES ('{1 mon,2 hours}'), ('{3 days}');
-- after the page goes ALL_VISIBLE:
SELECT i FROM av WHERE i = '{3 days}';
XX000: IOS decode: btree: interval key is the comparison span
       (interval_cmp_value) and cannot be decoded back to month/day/time
```

The same failure applies to every element type `decodeArrayKeyElemText` refuses:
`date[]`, `time[]`, `timetz[]`, `timestamp[]`, `timestamptz[]`, `bytea[]`,
`interval[]`. A refusal reached mid-decode is not a slower plan — it is `XX000`
for the whole statement.

## The shape of the fix

Decodability is a property of the key layer, so the key layer answers it:
new `internal/executor/btree_key_decodable.go`,
`indexKeyColumnIsDecodable(col catalog.Column) bool`, consulted by
`indexOnlyScanOp.indexKeyIsDecodable` in place of the hand-written type test.

For an array column it recurses exactly as `decodeArrayBTreeKey` does — an array
key is invertible iff its elements are, both as VALUES (the element's own scalar
key) and as TEXT. The text half was previously buried inside
`decodeArrayKeyElemText`'s trailing `switch`, so this slice lifted the rendering
table out as `arrayKeyElemRenderer(name) func(Datum) (string, error)`, returning
`nil` for an element goopg cannot spell the way the heap-side array decode spells
it. One table now both renders and predicts, which is what keeps the predicate
from drifting away from the decoder it speaks for.

### Why the refused element types stay refused

The ledger row this slice closes (2026-08-11) proposed adding an `interval` arm
to `decodeArrayKeyElemText` rendering `formatInterval`. **That resume point was
wrong**, and the reason generalises:

- `interval` / `timetz` elements: the SCALAR key is not invertible at all.
  interval's key is upstream's `interval_cmp_value` span, which has already
  collapsed `'1 mon'` and `'30 days'` into the same 128-bit value — deliberately,
  because PG calls them equal. There is no month/day split left to render, so an
  arm would print *a* legal spelling of the span, not the stored one.
- `date` / `time` / `timestamp` / `timestamptz` / `bytea` / enum elements: the
  key decodes, but goopg's heap array codec has no element arm for these types
  (`pgarray.ElemTypeInfo`), so a stored `date[]` holds the user's own literal
  spelling as TEXT while a key-side arm would render the canonical one. Adding
  the rendering would make the index and the heap disagree about the same
  value's text — the exact hazard `decodeArrayKeyElemText`'s contract exists to
  prevent.

Declining is therefore the correct end state for all of them until the heap
codec grows the matching element images; the defect was never the refusal, it
was the scan not knowing about it in time.

## The sibling that had drifted: `uuid`

Writing the parity gate surfaced a second, independent defect in the same
family (Hard-won Rule #2). The two blob decoders are
`decodeBTreeKeyToDatum` (single-column IOS lane) and `decodeIndexKeyColumn`
(composite walk, also the amcheck comparator's). `uuid` rides `EncodeVarchar`
— its canonical lowercase-hex text compares exactly as `uuid_cmp`'s memcmp does
— and `decodeIndexKeyColumn` has always listed it with the text-likes.
`decodeBTreeKeyToDatum` never did, so a uuid key fell through to that function's
`default:` arm, which reads any 8 leading bytes as an **enum** float8 sort order
and never errors: the single-column lane answered an empty `KindEnum` Datum for
a real uuid.

It is latent at HEAD only because a uuid index takes the PG tuple-image key path
(`pgIndexTupleKeys`, M0130-S11.4) and never reaches the blob lane — which is
precisely how the blob sibling drifted unnoticed. Fixed by adding `uuid` to that
`case`, the same arm its sibling already had.

## Gates

All three confirmed non-vacuous by source mutation.

| gate | asserts | mutation that caught it |
|---|---|---|
| `TestArrayIndexOnlyScanReadsHeapForRefusedElement` (`btree_array_key_indexonly_test.go`) | end to end: an indexed `interval[]` / `date[]` column answers `SELECT … WHERE a = …` from the heap, IOS-promoted, ALL_VISIBLE | restoring `!col.Type.IsArray && isIntervalTypeName(…)` reproduces the original `XX000` for both element types |
| `TestIndexKeyDecodableMatchesDecoder` (`btree_key_decodable_test.go`) | for 20 types × {scalar, array}: the predicate agrees with what BOTH decoders actually do, in both directions; plus a degeneracy check that the table exercises refusals on more arrays than scalars | dropping the array-element check makes the predicate over-claim for 7 array types |
| `TestIndexKeyDecodeSiblingsAgree` (same file) | wherever a key is decodable, the single-column lane and the composite lane return the same Kind and the same text | removing the new `uuid` arm reports `single-column 9/"" vs composite 3/"0000…0001"` |

Package gates: `go test ./internal/executor/`, `./internal/amcheck/`,
`./internal/access/btree/`; the units pre-commit scope; `tpch-spotcheck.sh`
(the change is on an executor decode path).

## Ledger

The 2026-08-11 row proposing an `interval` arm for `decodeArrayKeyElemText` is
resolved as **retracted, not implemented** — the resume point it named would
have produced a spelling the heap does not hold. Two rows remain open: the heap
array codec has no `date`/`time`/`timestamp`/`bytea`/enum element images (which
is what keeps those element types refused on the key side), and an array-keyed
index still costs heap fetches where PG can answer index-only, the same
consequence the interval scalar row already records for
`indexKeyIsDecodable` — it retires with the per-attribute index-tuple format.
