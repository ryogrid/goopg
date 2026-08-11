# M0119-0006 — array index keys render date/time/timestamp/timestamptz/bytea elements again

**Status:** landed 2026-08-12 (27th slice of M0119-0006)
**Code:** `internal/executor/btree_array_key.go`,
`internal/executor/btree_key_decodable.go` (unchanged, but its answer moves),
`internal/executor/btree_array_key_decode_test.go`,
`internal/executor/btree_array_key_indexonly_test.go`
**Predecessors:** `0119-0006-array-index-key-encoding.md` (the key form),
`0119-0006-array-index-key-decodability.md` (the ahead-of-scan predicate),
`0119-0006-array-element-datetime-images.md` (the heap element images this slice
depends on)

## What this slice changes

`arrayKeyElemRenderer` — the single table that both renders an array element out
of a B-tree key and answers, ahead of any scan, whether that element *can* be
rendered — gains arms for `date`, `time`, `timestamp`, `timestamptz` and
`bytea`. `indexKeyColumnIsDecodable` consults the same table, so an index over
any of those five array types is back on the index-only-scan fast path:

```sql
CREATE TABLE t (a date[]);
CREATE INDEX ON t (a);
-- page ALL_VISIBLE:
SELECT a FROM t WHERE a = '{2021-03-04}';   -- answered FROM THE KEY, no heap fetch
```

Before this slice the predicate declined the index for these types and every row
cost a heap fetch that PostgreSQL does not pay.

## Why they were refused, and why the reason is gone

The 25th slice refused them for a specific, correct reason, recorded in the
file's own comment: *there was no heap element image for the key text to agree
with*. goopg's array codec had no `pgarray.ElemTypeInfo` arm for any date-time
type or `bytea`, so a stored `date[]` held the characters the user typed as
**text** (`{2020-1-2}` stayed `2020-1-2`), while a key-side renderer would have
produced the canonical `2020-01-02`. Rendering from the key would have made the
same row print differently depending on which plan the planner chose.

The 26th slice removed that asymmetry: those six element types now store
upstream's own physical image (int32 days / int64 micros / int64+int32 timetz /
raw bytes), decoded through `pgdatetime.Format{Date,Time,Timestamp,
TimestampTZUTC}` and `pgarray.ByteaOutHex`. There is now exactly one spelling per
value, and the key side can produce it.

Two element types stay refused, for the *other* reason, which no heap image can
repair: `interval` and `timetz` keys are lossy comparison spans
(`interval_cmp_value`; timetz's GMT-equivalent time with the zone folded into
it), so the key never carried what the text needs.

## How the renderers avoid sibling drift

The renderers do not re-derive the text. Each converts the key's Datum back to
the *heap image* — `pgTimestampMicrosOfDatum`, `pgTimeMicros`, days-since-epoch,
raw bytes — and calls the very leaf function the heap-side element decode calls
(`pgarray.decodeArrayElem`). Two tables producing the same text by construction,
not by review; Hard-won Rule #2 applied to a pair that has already drifted once
in this milestone.

Each arm is additionally gated on `pgarray.ElemTypeInfo(name)` reporting an arm.
A type-name spelling the heap table does not know (`timestamp without time
zone`) is encoded by the heap through its **text fallback**, so rendering it
canonically here would re-introduce exactly the disagreement the 25th slice
declined — the gate makes the key side refuse whatever the heap does not image.

`date` deliberately computes its day count from `Unix()` seconds rather than
`UnixMicro()`: PG's date range reaches 5874897 AD, whose microsecond count
overflows `int64`.

## What the gates pin

- `TestArrayKeyTextMatchesHeapText` (new) — for every indexable array type, the
  text reconstructed from the KEY equals the text read back from the HEAP.
  This is the property the whole slice turns on, asserted directly instead of by
  reading two tables side by side. Mutation-checked (a `FormatTimestamp` →
  `FormatTimestampTZUTC` swap fails it).
- `TestArrayIndexOnlyScanAnswersFromKey` (new) — end-to-end per type: the plan
  **must** be an `IndexOnlyScan` (a `Fatalf`, not a skip) and the row must equal
  PG 18.3's `array_out` spelling, captured from the reference cluster on port
  65432: `{2021-03-04}`, `{04:05:06}`, `{"2021-03-04 05:06:07"}`,
  `{"2021-03-04 05:06:07+00"}`, `{"\\x0304"}`.
- `TestArrayIndexOnlyScanReadsHeapForRefusedElement` — keeps the declining
  direction honest; its `date` case moved to the test above and was replaced by
  `timetz`, which is refused for the surviving structural reason.
- `TestIndexKeyDecodableMatchesDecoder` / `TestIndexKeyDecodeSiblingsAgree`
  (pre-existing) — the predicate still agrees with both decoders.

## Found while gating: `numeric[]` prints a different scale from an index-only scan

The new heap/key parity gate immediately caught a divergence this slice did not
introduce and does not fix:

| path | `ARRAY[1.50::numeric]` |
|---|---|
| PG 18.3 | `{1.50}` |
| goopg heap | `{1.50}` |
| goopg index-only scan | `{1.5}` |

`btree.EncodeNumericKey` strips trailing mantissa zeros — value-preserving, as
its own comment says, but PostgreSQL's `numeric` carries **display scale** as
part of the value, so the key path loses user-visible information. The same
applies to a scalar `numeric` column under an index-only scan.

It is deferred rather than fixed here because the obvious containment — having
`indexKeyColumnIsDecodable` refuse `numeric` — must, to keep
`TestIndexKeyDecodableMatchesDecoder`'s contract, also make
`decodeIndexKeyColumn` refuse it, and that decoder is the composite walk
`btIndexOpClassComparator` uses, so `bt_index_check` would stop working on every
`numeric` index. The real fix is a key encoding that carries the scale. The gate
asserts the divergence explicitly, so the day it is fixed the test says so.
Ledger row 2026-08-12.
