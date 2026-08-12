# 0119-0006 — The numeric index key has no display scale, and cannot be given one

**Status:** accepted — landed (M0119-0006, 34th slice)
**Date:** 2026-08-13
**Milestone:** M0119-0006 (deferral-ledger backlog consumption; source M0110-0003)
**Supersedes the resume point of:** deferral ledger row `2026-08-12 | M0119-0006`
("carry the display scale in the key"), which this slice measured and found
unimplementable — see §3.

## 1. The defect

An index-only scan over a `numeric[]` column printed a different value than every
other plan over the same row:

```sql
CREATE TABLE t (a numeric[]);
CREATE INDEX ON t (a);
INSERT INTO t VALUES ('{2.70}');
VACUUM t;                        -- page becomes ALL_VISIBLE
SELECT a FROM t WHERE a = '{2.70}';
--  IndexOnlyScan  ->  {2.7}     -- goopg, before this slice
--  any other plan ->  {2.70}    -- goopg's heap decode, and PG 18.3
```

Not a failed query and not a slow plan: the *same stored row* spelled two ways
depending on which plan the planner picked, silently, with no error anywhere.
PG prints `{2.70}` because `numeric` carries its DISPLAY SCALE as part of the
value and `numeric_out` prints what was stored.

The scalar `numeric` arm is exposed by the same reasoning but was not reproducing
on the fixture used here, because that index takes the PG tuple-image key path
(§4). The fix covers both; the test carries both arms.

## 2. Why the key does not have the scale

`btree.EncodeNumericKey` (`internal/access/btree/btree.go`) strips trailing
mantissa zeros before emitting its digit run. That is not an oversight — it is
the property the encoding exists for. goopg's blob index key is order-preserving
BYTES, and equality is byte equality, so two numerically equal values must encode
identically:

```
EncodeNumericKey(10, 1)   -- 1.0
EncodeNumericKey(100, 2)  -- 1.00
    both -> [02 80 00 00 00 '1' 00]
```

That byte identity is what makes `INSERT 1.00` after `INSERT 1.0` raise 23505 on
a `UNIQUE` numeric column, which is what PG does (`numeric_cmp` ignores display
scale). Measured at HEAD before the change, not assumed.

## 3. The resume point the ledger recorded is unimplementable

The 2026-08-12 ledger row proposed carrying the display scale in the key as
trailing metadata, "so it need not disturb the order-preserving mantissa run".
Order is indeed undisturbed — but the constraint that actually binds is not order,
it is EQUALITY. Byte-identical keys cannot also distinguish two spellings of one
number. Appending the scale (leading, trailing, anywhere) makes `1.0` and `1.00`
encode differently, and a UNIQUE index on `numeric` then admits both. The two
goals are mutually exclusive under a byte-comparable key; upstream avoids the
conflict by storing the datum itself and comparing through `numeric_cmp`, which
is a different architecture, not a smaller change.

So the display scale is not recoverable from the key. It still exists — in the
heap.

## 4. The fix: split the two questions the scan was asking as one

An index-only scan substitutes the key for the heap, so it needs two things from
a key column, and goopg had conflated them into `indexKeyColumnIsDecodable`:

| question | who needs it | numeric |
|---|---|---|
| can the bytes be inverted to the right VALUE? | `bt_index_check`'s operator-class comparator (`decodeIndexKeyColumn`) | **yes** |
| does that Datum SPELL the value the way the heap spells it? | the index-only scan's projection | **no** |

`internal/executor/btree_key_decodable.go` now answers both:

- `indexKeyColumnIsDecodable` — unchanged. Still the decoder's own contract, still
  pinned by `TestIndexKeyDecodableMatchesDecoder` (predicate ⇔ decode returns no
  error). `numeric` stays decodable.
- `indexKeyColumnRendersHeapText` — new, strictly narrower, refuses `numeric`.
  `Type.Name` is the ELEMENT type name for an array column, so the single test
  covers `numeric` and `numeric[]` alike.

`indexOnlyScanOp.indexKeyIsDecodable` requires both, and asks the second only of
the BLOB key format: on the `pgIndexKeyDesc` (PG tuple-image) path the key carries
per-attribute datums, so no spelling is lost, and `decodeRowFromKey` routes on the
same descriptor — the predicate and the decoder therefore judge the same decode.
A refused index reads the heap, exactly as `interval[]` and `timetz[]` do.

This is why the refusal is in the PREDICATE and not in the decoder. The
containment the ledger row weighed — making `decodeIndexKeyColumn` refuse
`numeric` so the old single predicate stayed honest — would have taken
`bt_index_check` down on every numeric index, in exchange for nothing: a
value-correct decode is all a comparator ever needed.

## 5. Cost

Index-only scans over `numeric`/`numeric[]` columns stored in the blob key format
now pay a heap fetch per row. That is the price of printing the right number, and
it is the same price `interval` has paid since the 17th slice. Indexes in the PG
tuple-image format are unaffected.

## 6. Gates

- `TestNumericIndexOnlyScanKeepsDisplayScale` (E2E, scalar + array) — the defect.
  Mutation-checked: with the renderability check removed the array arm reports
  `row="{2.7}" want "{2.70}"`.
- `TestNumericUniqueCollapsesDisplayScale` — the other end of the trade, so a
  future attempt to append the scale to the key fails here instead of silently
  admitting a duplicate key.
- `TestArrayKeyTextMatchesHeapText` — its `scaleLossyType` exception is gone; the
  loop is now driven by the new predicate, and its non-vacuity block asserts
  `numeric[]` is refused for the RENDERING reason while remaining DECODABLE.
- `TestIndexKeyRenderableIsNarrowerThanDecodable` — the containment between the
  two predicates, which are edited independently.
- `TestIndexKeyDecodableMatchesDecoder`, `TestIndexKeyDecodeSiblingsAgree` —
  unchanged and still green, which is the evidence the comparator lane was not
  touched.
- `scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35); TPC-DS SF0.5 sweep PASS=95
  MISMATCH=0 CKMISMATCH=0, plan shapes identical (99/99); UNITS PASS.

## 7. Still open

`numeric` is now the only type that is decodable-but-not-renderable. Closing it
for real means giving goopg a key format that stores the datum and compares
through a type-aware comparator (upstream's model) rather than through bytes —
the PG tuple-image path is the existing seam, and widening it to array key
columns would restore the fast path here as a side effect. Ledger row filed.
