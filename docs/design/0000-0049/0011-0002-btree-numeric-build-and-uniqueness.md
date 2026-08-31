# 0011-0002 — B-tree NUMERIC Build and UNIQUE/PRIMARY KEY

**Status:** accepted
**Milestone:** [0011 — B-tree NUMERIC Key Support](../../milestones/0011-btree-numeric-key-support.md)
**Spans seam:** DDL acceptance, backfill, variable-length HighKey, index-scan lookup
**Cross-links:**
[0011-0001](0011-0001-btree-numeric-key-ordering.md) (encoding contract),
[0002-0002](0002-0002-btree-concurrency.md) (HighKey + Lehman-Yao right-link),
[0003-0012](0003-0012-numeric-arithmetic.md) (NUMERIC datum carrier).

## Context

`0011-0001` defined `EncodeNumericKey(mantissa, scale)` — a sortable byte
encoding for NUMERIC. This slice wires that encoding into the live DDL,
backfill, and index-scan paths so HammerDB TPC-H's
`CREATE INDEX … ON … (l_orderkey numeric)` no longer aborts with
`btree v0 only supports int4 keys`.

The blocking pieces are:

1. `createSingleColumnBTreeIndex` rejects any non-int4 key column.
2. `backfillSingleColumnBTree` extracts an `int32` from the heap and uses
   an `int32`-keyed `seen` map for UNIQUE.
3. `indexScanOp.lookupKey` requires `KindInt` and encodes via
   `EncodeInt4`.
4. `BTPageOpaque.HighKey` is a fixed `[4]byte` field on disk — fine for
   int4 (always 4 bytes) but truncates anything wider, and the split
   path explicitly errors when `len(rightItems[0].key) != 4`.

(4) is the only structural change; (1)–(3) are localised branches.

## Changes

### Variable-length HighKey on disk

`BTPageOpaque.HighKey` becomes `[]byte` in memory. On disk the opaque
area grows from 24 bytes to 48 bytes:

```
offset 0   Prev        (4 bytes)
offset 4   Next        (4 bytes)
offset 8   Level       (4 bytes)
offset 12  Flags       (2 bytes)
offset 14  HighKeyLen  (2 bytes)         <- new: replaces _padding
offset 16  HighKey     (MaxHighKeyLen=32 bytes; bytes 0..HighKeyLen valid)
```

`SizeOfBTPageOpaque` = 48. `MaxHighKeyLen` = 32 (covers `EncodeInt4`
4 bytes and `EncodeNumericKey` ≤25 bytes with headroom).
`btreeVersion` bumps from 2 to 3 — older on-disk btrees error with
`ErrNotABTree` and require a fresh CREATE INDEX. goopg is pre-GA so
no migration tooling lands; the version bump is the migration story.

`HighKeyLen == 0` is reserved to mean "no high key" (paired with
clearing `BTHasHighKey`). Larger keys than `MaxHighKeyLen` panic the
split path — by construction (int4 is 4 bytes, NUMERIC ≤25) this can
only happen if a future key encoding goes wider; the explicit check
documents the invariant.

The split path (around `internal/access/btree/btree.go:786`) drops the
`len(rightItems[0].key) != len(op.HighKey)` length-equality guard and
just records the right page's smallest item as the new HighKey.

### DDL acceptance

`createSingleColumnBTreeIndex` accepts `numeric` / `decimal` in addition
to int4 types. The check moves from a hard `isInt4Type` rejection to a
`supportedBTreeKeyType` predicate that returns true for both families;
unsupported types still error with `0A000`.

### Backfill

`backfillSingleColumnBTree` branches on the column type:

- **int4**: existing `int32` extract + `EncodeInt4` + `seen[int32]` —
  unchanged path so int4 indexes are byte-for-byte compatible with
  the pre-change behaviour (regression guard).
- **numeric**: `KindNumeric` extract → `EncodeNumericKey(m, s)` →
  byte-string-keyed `seen[string]` so `(10,1)` and `(100,2)` collapse
  into the same dedup entry. `KindInt` rows in a numeric column are
  promoted via `numericFromInt` (the numeric module's own promotion)
  before encoding.

Encoded keys flow into `tree.Insert(key, ptr)` — the variable-length
key path is already supported by `item.marshal` /
`pageHasSpaceFor`; only the HighKey was the structural blocker.

### Index scan

`indexScanOp.lookupKey` mirrors backfill: numeric column → encode via
`EncodeNumericKey`, int4 column → existing int32 path. The column type
is reachable via `o.plan.Index` → catalog lookup; for v0 the planner
already checks the index column type when it emits `IndexScan`, so this
path receives a Datum whose Kind matches the index's key type.

## Tests

### B-tree package (variable-length HighKey)

- `TestSplitWithVariableLengthHighKey`: insert enough variable-length
  numeric-encoded keys to force a split, then verify Search returns
  every inserted key. Without the HighKey widening, the split path's
  length-equality guard would trip.
- `TestOnDiskOpaqueRoundTrip`: write opaque with a 25-byte HighKey
  through `writeOpaque` / `readOpaque`, confirm round-trip.

### DDL + backfill

- `TestCreateNumericBTreeIndexAcceptsType`: `CREATE INDEX` on a
  `numeric` column succeeds (was rejected before).
- `TestNumericUniqueIndexCollapsesScales`: insert rows with
  `1.0`, `1.00`, `1.000` text values; UNIQUE INDEX build rejects with
  23505. Without the byte-string dedup map this passes incorrectly.
- `TestInt4PathUnchanged`: regression guard — int4 CREATE INDEX still
  works end-to-end.

### Index scan

- `TestIndexScanFindsNumericKey`: build numeric index, query with a
  literal whose scale differs from the stored scale (e.g. stored
  `1.0`, query `1.00`); the scan returns the row.

## Out of scope

- Multi-column composite numeric keys (B-tree v0 is single-column).
- ALTER TABLE … ADD UNIQUE on existing data with NULL handling
  beyond what int4 already supports.
- HighKey storage growing beyond 32 bytes — would require yet another
  format bump. NUMERIC tops out at 25, so 32 is sufficient and any
  future overflow is caught explicitly.
- HammerDB TPC-H end-to-end validation (M0011-0003).
