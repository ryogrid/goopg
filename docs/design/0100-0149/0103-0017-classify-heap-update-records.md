# 0103-0017 — Classify HeapUpdate / HeapHotUpdate records into ChangeUpdate

Status: accepted
Milestone: M0103-0008 (rung 12 closure, partial — UPDATE half).

## Problem

After rung 11 closed the `publication_names` parsing bug, dropping the
`t.Skip` on `TestPort_PgoutputInteropGoopgToPG` showed that Insert (`I`,
kind 4) and Delete (`D`, kind 6) pgoutput records now flow from a goopg
publisher to a PG subscriber, but UPDATE rows do not. Diagnostic logging
added to `internal/wal/pgoutput.go::Change` confirmed the change events
never reach the plugin: `internal/wal/classifier.go::Classify` only
switches on `RecordKindHeapInsert`, `RecordKindHeapDelete`,
`RecordKindXactCommit`, and `RecordKindXactAbort`. Both runtime UPDATE
emission paths in the executor (`writeHeapRow` for non-HOT, the in-place
HOT path in `operators_storage.go`) write `RecordKindHeapHotUpdate`
(kind 13) or `RecordKindHeapUpdate` (kind 27); both fall into the
classifier's default branch and are silently dropped.

## Design

Two new cases on the `r.Payload[0]` switch in `Classify`:

1. `RecordKindHeapHotUpdate` (`DecodeHeapHotUpdate` →
   `rel, blk, oldSlot, xmax, tupleBytes`): the new-tuple bytes carry
   the updating xact's `xmin` at offset 0 (heap-tuple binary layout —
   see `internal/storage/heap.go::NewHeapTuple`). Reuse the existing
   `xminFromTuple` helper to pull the xid; route a `Change{Kind:
   ChangeUpdate, NewTuple: tupleBytes, Block: blk, LineSlot: oldSlot}`
   through `Decoder.ApplyChange`. `OldTuple` stays empty — HOT-update
   records do not carry the pre-image (only the old slot offset).

2. `RecordKindHeapUpdate` (`DecodeHeapUpdate` → `HeapUpdatePayload`):
   same xid-extraction strategy on `p.Tuple`. Route `Change{Kind:
   ChangeUpdate, Rel: p.Rel, Block: p.NewBlk, LineSlot: p.NewLineSlot,
   NewTuple: p.Tuple}`.

`OldTuple` is intentionally absent. Under `REPLICA IDENTITY DEFAULT` —
the only identity goopg currently surfaces in pgoutput's relation
descriptor (`writeRelation` always emits `'d'` and marks every column
as part of the identity set) — upstream's `logicalrep_write_update`
skips the K/O block when no replica-identity column changed. The
matching path in `internal/wal/pgoutput.go::writeUpdate` already handles
`len(oldTuple) == 0` correctly (rung 9 fix): it emits
`'U' relOid 'N' newTuple` directly, byte-identical to upstream. A PG
subscriber resolves the row to update by re-keying off the new tuple's
identity columns against its own primary key — sufficient for the
interop harness, which uses `(id int PK, v text)`.

Other record kinds (`RecordKindPageImage`, `RecordKindBtreeInsert`,
`RecordKindHeapVacuum`, `RecordKindCheckpoint`) remain silently
skipped. PageImage emission for fresh-page inserts is a separate
discussion (the executor's `markHeapInsertDirty` does call the heap-
insert hook, so the rung-11 diagnostic's PageImage claim may have been
inaccurate; if subsequent live runs confirm a real gap, it will land
under its own rung).

## Tests

Two new tests in `internal/wal/classifier_test.go`:

- `TestClassifyHeapHotUpdateRoutesByXmin`: encode a `HeapHotUpdate`
  whose new tuple carries `xmin=55`; verify Classify dispatches a
  `ChangeUpdate` queued under xid 55, with `NewTuple` matching the
  encoded bytes; commit and verify the plugin sees
  `Begin / Change / Commit`.

- `TestClassifyHeapUpdateRoutesByXmin`: same shape with a non-HOT
  `EncodeHeapUpdate(HeapUpdatePayload{...})`, xmin=66.

Plus a pin to the existing `TestClassifySkipsNonTxRecords` set is
explicitly not added — HeapUpdate/HotUpdate are now actionable events,
not silent skips.

## Verification

```
go test -race -count=1 ./internal/wal/ ./internal/server/ \
  ./internal/executor/ ./internal/catalog/
```

## Out of scope (next rung)

- Page-image synthesis. If a runtime trace confirms that fresh-page
  inserts emit `RecordKindPageImage` instead of (or in addition to)
  `RecordKindHeapInsert`, the next rung will either:
  (a) extract tuple slots from the page image and synthesise
      `ChangeInsert` events per slot, or
  (b) modify the executor's first-INSERT-into-fresh-page path to write
      a plain `RecordKindHeapInsert` so the classifier sees the same
      shape upstream PG produces.

- Replica-identity-FULL old-tuple propagation. When goopg starts
  honouring `REPLICA IDENTITY FULL` (0008-0003), HOT-update records
  will need a payload extension carrying the pre-image bytes; the
  classifier's `OldTuple` field already exists for that day.
