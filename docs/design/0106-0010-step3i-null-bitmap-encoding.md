# M0106-0010 step 3i — PG-conformant null bitmap encoding

Status: accepted (landed 2026-05-17)
Milestone: M0106-0010 (pg_am + nailed-catalog bootstrap)
Step: 3i (depends on 3g — `Form_pg_index` encoder)

## Problem

Step 3g landed `Form_pg_index` rows for every nailed local + shared index
and was supposed to close the `FATAL: cache lookup failed for index 2662`
PG-standby boot blocker exposed by Step 3f. After 3g landed, the same
FATAL persisted: `TestE2E_FailoverGoopgToPG/async` aborted with PG
backends crashing at the `SearchSysCache1(INDEXRELID, 2662)` lookup that
walks the pg_index heap.

Inspecting the on-disk first tuple of `base/5/2610` (offset 8030, 162
bytes) revealed two interacting bugs in goopg's heap-tuple encoder that
silently corrupted any tuple containing a NULL column. `pg_index` was
the first catalog where goopg seeded NULL columns (`indexprs` and
`indpred`, both `pg_node_tree`) — every other bootstrapped catalog
(pg_class, pg_am, pg_proc, pg_opclass, pg_amop, pg_amproc) had filled
all columns with concrete values, masking the bug.

### Bug 1 — inverted null bitmap convention

`internal/executor/codec.go::encodeRowPG` set the bitmap bit when the
column **was** NULL:

```go
if d.IsNull() {
    nullBitmap[i/8] |= 1 << (i % 8)
}
```

PG's `heap_fill_tuple` (`postgres/src/backend/access/common/heaptuple.c`
line ~308) does the opposite: the bit is **cleared** on NULL columns
and set on NOT-NULL columns. `att_isnull` reads it as `!((bits[ATT>>3]
& (1 << (ATT & 7))))`.

For a 21-column pg_index row with cols 20, 21 NULL, goopg emitted
bitmap `{0x00, 0x00, 0x18}` (bits 19 and 20 set, all others clear).
PG read this as "cols 1-19 are NULL, cols 20-21 are NOT NULL" —
including `indexrelid`, which made every `SearchSysCache1(INDEXRELID,
…)` lookup return SearchSysCacheMiss.

### Bug 2 — bitmap stored in the wrong place

`encodeRowPG` prepended the bitmap bytes to the column data area and
returned the concatenated payload. `writeMultiPageHeapRows` then handed
that payload to `storage.NewHeapTuple`, which sets `t_hoff =
DefaultHeapTupleHoff = 24` (`MAXALIGN(SizeofHeapTupleHeader)` with no
bitmap). The header also did **not** set `HEAP_HASNULL`. Final tuple:

```
bytes 0-22:  HeapTupleHeaderData
byte  22:    t_hoff = 24
byte  23:    1 byte alignment pad
bytes 24+:   [bitmap (3 bytes)] [pad to 4-align] [indexrelid (4 bytes)] ...
```

PG expected:

```
bytes 0-22:  HeapTupleHeaderData (t_infomask |= HEAP_HASNULL)
byte  22:    t_hoff = MAXALIGN(23 + 3) = 32
bytes 23-25: null bitmap (PG convention)
bytes 26-31: alignment pad
bytes 32+:   column data starting at indexrelid
```

`heap_deform_tuple` parsed the goopg layout as if `t_hoff = 24` and no
bitmap was present, reading the bitmap bytes themselves as the first
columns of the tuple — every catalog field landed off by `bitmap +
pad` bytes.

## Fix

### `internal/storage/heap.go`

- Add `Bitmap []byte` field to `HeapTuple`. The bitmap is now stored
  separately from the column data area so `MarshalBinary` can place it
  at the canonical PG location (right after the fixed 23-byte header).
- Add `NewHeapTupleWithNulls(xmin, xmax, bitmap, data)` constructor
  that sets `t_hoff = MAXALIGN(SizeofHeapTupleHeader + len(bitmap))`
  and stamps `HEAP_HASNULL` in `Infomask`.
- Add unexported `maxAlign8(n)` helper (`(n + 7) &^ 7`, mirroring PG's
  `MAXALIGN` on 64-bit platforms — goopg only targets 64-bit).
- Update `MarshalBinary` so that when `Bitmap` is non-nil it copies the
  bitmap into `out[SizeOfHeapTupleHeaderData : ...]` before copying
  `Data` at `out[hoff:]`.
- Update `parseHeapTupleAlias` (and via it `ParseHeapTuple`) to
  populate `Bitmap` from `raw[23:23+(natts+7)/8]` whenever
  `HEAP_HASNULL` is set — keeps round-tripping intact for the
  recovery / WAL replay paths that re-read what we write.

### `internal/executor/codec.go`

- Strip the bitmap path out of `encodeRowPG`: it now returns the
  column-data area only (NULL columns contribute 0 bytes, mirroring
  PG's heap_fill_tuple behavior).
- Add `NullBitmapPG(row Row) []byte` which returns the PG-convention
  bitmap (bit=1 = NOT NULL), or nil when the row has no NULLs.
- Update `EncodeRowPG`'s doc comment to point callers at
  `NullBitmapPG` + `storage.NewHeapTupleWithNulls`.

### `internal/initdb/initdb.go`

- `writeMultiPageHeapRows` now calls `executor.NullBitmapPG(row)` per
  row. When the bitmap is non-nil, it constructs the tuple via
  `storage.NewHeapTupleWithNulls` (which stamps `HEAP_HASNULL` and the
  correct `t_hoff`); otherwise it falls back to the existing
  `storage.NewHeapTuple` path so non-NULL catalogs are byte-identical
  to before.

## Regression pins

- `TestNewHeapTupleWithNullsLayoutMatchesPG18`
  (`internal/storage/heap_nullbitmap_test.go`) pins t_hoff, bitmap
  location, alignment pad, data location, and bitmap-roundtrip for the
  canonical 21-column pg_index shape.
- `TestHeapTupleNullBitmapConventionMatchesPG18` pins the
  byte-level bitmap convention (bit=1 = NOT NULL).
- `TestNullBitmapPGUsesPGConvention`,
  `TestNullBitmapPGNilWhenNoNulls`,
  `TestNullBitmapPGSpansTwoBytes`
  (`internal/executor/codec_nullbitmap_test.go`) pin
  `executor.NullBitmapPG`'s output against the PG convention and the
  pg_index-shaped 21-column row.

These pins are deliberately phrased in terms of PG18's
`heap_fill_tuple` semantics so future encoder changes get caught
before they re-introduce the inverted-bitmap regression.

## Verification

- `go test -count=1 ./internal/storage/ ./internal/executor/` — PASS
  (including the four new bitmap tests).
- `go test -count=1 -run
  'TestPgIndex|TestBootstrapPgIndex|TestPgAm|TestPgOpclass|TestPgProc'
  ./internal/initdb/` — PASS (all Step 3a/b/c/d/e/f/g pins still
  agree on layout).
- `GOOPG_RUN_BLOCKED_M0102_E2E=1 go test -count=1 -run
  'TestE2E_FailoverGoopgToPG/async' ./internal/testport/` — PG standby
  now reads pg_index correctly. The "cache lookup failed for index
  2662" FATAL is gone. The test advances to the **next** blocker,
  `FATAL: relnatts disagrees with indnatts for index 2662`, which is a
  separate pg_class/pg_index consistency issue tracked as Step 3j.

## Why this is its own step

The fix touches three packages (`internal/storage`, `internal/executor`,
`internal/initdb`) and reshapes a load-bearing struct (`HeapTuple`
gained a `Bitmap` field). Bundling it with Step 3g would have hidden
the encoder bug inside a much larger pg_index commit; isolating it
makes the failure mode and the fix traceable when the same shape comes
up again for pg_constraint, pg_rewrite, pg_trigger, or any other
catalog that bootstrap seeds with NULL columns.
