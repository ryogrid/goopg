# 0119-0006 — B-tree key encoding for ARRAY key columns (`array_ops`)

**Status:** accepted (2026-08-10)
**Milestone:** M0119-0006 (pg_amcheck server tier) — the `int4[]` key-encoding
row of the type cluster
**Code:** `internal/executor/btree_array_key.go`,
`internal/executor/operators_ddl.go` (`encodeBTreeKeyForColumn`)
**Gates:** `TestEncodeArrayBTreeKeyMatchesArrayCmpOrder`,
`TestEncodeArrayBTreeKeyTextElements`,
`TestEncodeArrayBTreeKeyDeclinesMultidim`,
`TestArrayIndexBuildAndMaintainKeys`,
`TestArrayIndexCompositeKeyIsSelfDelimiting`

## 1. The defect

A goopg array column carries `catalog.Type{Name: <ELEMENT type>, IsArray: true}`
— `int4[]` is `Type{Name:"int4", IsArray:true}` (`internal/executor/codec_array.go`).
Every type predicate in `encodeBTreeKeyForColumn` tests `col.Type.Name`, so
`isInt4Type("int4")` answered **true for the array column** and the array's
runtime value — its canonical text `"{1,2}"` as a `KindString` datum — was fed
to the scalar int4 arm. The same `Name`-only test in
`isSupportedBTreeKeyType` let `CREATE INDEX` through in the first place.

The two write paths then failed differently, and both silently:

| path | pre-fix behaviour |
|---|---|
| `CREATE INDEX` over existing rows (bulk build) | aborted with a bogus `22P02 invalid input syntax for type integer: "{1,2}"` — the unknown-literal coercion of the array text into an int4 |
| `INSERT` after the index exists (maintain path) | wrote **no index entry at all**: `maintainUniqueIndexesForInsert` swallows key-encode errors by design (`operators_storage.go`, M0100-0005) |

So `CREATE INDEX` on an *empty* table succeeded and produced an index that
stayed permanently empty: an index scan over it reads as "no rows" (wrong
results, not an error) and a `UNIQUE` array index enforces nothing. Verified at
HEAD before the fix with a throwaway physical-tree probe — five inserted rows,
zero leaf entries.

## 2. The order to reproduce

Upstream `array_ops` compares through `btarraycmp` → `array_cmp`
(`src/backend/utils/adt/arrayfuncs.c`):

1. element by element under the element type's comparator, over the first
   `min(nitems1, nitems2)` elements;
2. two NULL elements are equal, and **NULL sorts after not-NULL**;
3. on a tie, fewer elements sorts first (`nitems1 < nitems2 ⇒ -1`);
4. then `ndims`, then `dims[]`, then `lbound[]`.

Captured from the PG 18.3 reference cluster rather than derived by reading the
source (the test carries the query):

```
select x from (values ('{1,2}'::int4[]),('{10}'),('{2,0}'),('{}'),('{1}'),
                      ('{1,NULL}'),('{1,0}')) v(x) order by x;
 {} | {1} | {1,0} | {1,2} | {1,NULL} | {2,0} | {10}
```

## 3. The encoding

goopg's B-tree stores order-preserving key BYTES, so the order above has to be
expressed in the byte encoding rather than in a comparator:

```
non-NULL element:  0x01 ++ encodeBTreeKeyForColumn(element)
NULL element:      0xFF
end of array:      0x00
```

* **Element-wise (rule 1).** All present-tags are equal, so a comparison falls
  through to the first differing element's key bytes — which are the element
  type's own order-preserving encoding, produced by recursing into
  `encodeBTreeKeyForColumn` with the element type. Reusing that function rather
  than writing a private element encoder is what keeps an array key
  byte-identical to the scalar keys of its elements (the sibling-path rule the
  float and enum expression-key slices exist to enforce).
* **NULL last (rule 2).** `0xFF` is above the present tag, and the tag is the
  first byte of the element's segment.
* **Prefix first (rule 3).** Where the longer array continues with an element
  tag (`0x01`/`0xFF`), the shorter one has already emitted its end marker
  `0x00`, which is below both.

Neither marker is optional:

* Without the per-element tag, the NULL marker would be indistinguishable from
  an element whose encoding starts with the same byte — `EncodeInt4(maxint32)`
  is `0xFFFFFFFF`.
* Without the end marker the array segment is not self-delimiting, which breaks
  it as one column of a **composite** key: on `(a int4[], b int4)`,
  `('{1}', 2)` and `('{1,2}', 0)` would share a byte prefix and the shorter
  array would then be ordered by `b`'s leading byte. It also keeps `{}` a
  one-byte key instead of a zero-byte one — a zero-length key is
  indistinguishable from "no key" to `encodeIndexKeyFromCols` (nil slice), which
  dropped the empty-array row from the index entirely.

Element types are exactly the scalar-key-encodable set, because the element
encoding IS the scalar path: int4/int8/numeric/float/date/timestamp(tz)/
text/varchar/char/name/uuid. Element types with no scalar arm (bool, enum,
nested arrays) surface that arm's own `0A000`, and `isSupportedBTreeKeyType`
already rejects most of them at `CREATE INDEX` time via the element name.

## 4. Deliberately out of scope

* **Multidimensional arrays and non-default lower bounds** (rule 4). goopg's
  array codec only ever writes `ndim=1`/`lbound=1` (`encodeArrayValuePG`), so
  `array_cmp`'s dimension tie-breaks are unreachable from stored data; a nested
  literal is declined with `0A000` rather than flattened into a key that claims
  an order it does not have.
* **The decode side.** `decodeIndexKeyColumn` has no array arm, so amcheck's
  opclass comparator falls back to byte order for an index carrying an array key
  column. Byte order IS `array_ops` order after this change, so the fallback is
  correct for built-in classes; a *user* operator class on an array column is
  not dispatched. Ledger row 2026-08-10.
* **Quoted vs unquoted `NULL`.** `parseTextArray` (`expr.go`) discards quoting,
  so a `text[]` element that is the literal string `"NULL"` is indistinguishable
  from an array NULL at this layer. That is a pre-existing codec-level
  limitation, not introduced here; it is unreachable from stored data because
  `encodeArrayValuePG` rejects NULL elements outright.

## 5. Compatibility

This changes the on-disk key bytes for array index columns. No migration is
needed: before this change an array index was either impossible to build or
permanently empty, so no correct array index exists on disk to invalidate.
