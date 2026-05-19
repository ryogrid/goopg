# 0102-0006 — PostgreSQL Physical WAL Replay on goopg Standby (Stage 1)

**Status:** draft
**Date:** 2026-05-15
**Milestone:** M0102-0006
**Upstream reference:** `postgres/src/include/access/xlogrecord.h`, `postgres/src/include/access/heapam_xlog.h`, `postgres/src/include/access/xact.h`, `postgres/src/backend/access/heap/heapam_xlog.c`, `postgres/src/backend/access/transam/xlog.c`.

## Problem

Scenario A (`PostgreSQL primary -> goopg standby`) currently fails before the
standby can finish startup recovery. goopg already accepts PostgreSQL-style WAL
page headers and `XLogRecord` framing, but the recovery path still collapses
every `XLogRecord` into a single `Record.Payload` byte slice via
`decodeRecordXLog()`. That works only for goopg's own WAL, where the XLog main
data chunk is currently used as a wrapper around goopg's internal redo payload
bytes.

Upstream PostgreSQL physical WAL does not have that shape:

- records may contain zero or more block references before the main-data chunk,
- backup blocks / full-page images are carried in per-block headers,
- rmgr-specific main data (`xl_heap_insert`, `xl_xact_commit`, etc.) is not a
  goopg redo payload and must not be dispatched through `ApplyRecord`'s native
  `RecordKind*` switch.

The concrete failure proved during Scenario A debugging was a misleading native
decode path: a PostgreSQL main-data payload was fed into `DecodePageImage`,
which crashed with `wal: invalid page-image payload len 4`.

## Goal

Land the smallest executable replay slice that makes PostgreSQL physical WAL a
first-class input to the standby recovery path instead of an accidental native
payload.

Stage 1 delivers:

1. a decoder that preserves PostgreSQL record structure (rmgr, xl_info, block
   references, backup images, main data),
2. startup / iterator paths that keep those records intact instead of forcing
   them through `Record.Payload`,
3. physical replay for the subset needed by the first heterogeneous failover
   slice:
   - backup-block / full-page-image restore,
   - `RM_HEAP_ID / XLOG_HEAP_INSERT`,
   - `RM_XACT_ID` commit / abort markers as no-op physical records with replay
     metadata for standby MVCC visibility,
   - `RM_XLOG_ID` control records as recognised no-ops.

Everything else remains explicit `unsupported PostgreSQL WAL record` errors so
the standby fails closed with the rmgr/opcode surfaced in the error.

## Non-goals

Stage 1 does not attempt:

- general PostgreSQL heap update / delete / lock / prune / freeze redo,
- PostgreSQL btree redo,
- tablespace-aware replay beyond the default tablespace,
- WAL compression codecs on backup blocks,
- producer-side re-encoding of every goopg WAL mutation into upstream PostgreSQL
   rmgr-specific redo shapes.

## Design

### 1. Preserve PostgreSQL record structure

Add a PostgreSQL-aware decode layer under `internal/wal/` that parses the full
`XLogRecord` body:

- `XLogRecordBlockHeader`
- optional `XLogRecordBlockImageHeader`
- optional `RelFileLocator`
- block number
- block image bytes / per-block data bytes
- optional main-data chunk

The decoder preserves a canonical decoded `XLogRecord` shape for every
page-header-mode record:

- **main-data-only records**: exactly one main-data chunk, no block references.
   goopg's existing logical redo payload continues to surface through
   `Record.Payload` for compatibility with existing callers, while the decoded
   `XLogRecord` metadata is still retained.
- **block-referenced records**: records carrying block references, backup
   images, or other fragment layouts. These surface through the same decoded
   `XLogRecord` representation instead of being forced into `Record.Payload`.

This is the root fix: recovery, iterator, and write-position detection all stop
guessing that every PG-framed record is a goopg logical record.

### 2. Extend `Record` without breaking native callers

`Record` grows an optional decoded `XLogRecord` field, while existing callers
continue to consume `Payload` unchanged when the record is a main-data-only
logical redo record.

Conceptually:

```go
type Record struct {
    StartLSN uint64
    EndLSN   uint64
   Payload  []byte              // existing logical redo payload, when present
   XLog     *XLogDecodedRecord  // canonical decoded XLog metadata
}
```

`ReadAll`, `RecordIterator`, and the writer's restart scan all use the same
decoder so every PG-compatible page-header WAL record shares the same decoded
representation everywhere WAL is read.

### 3. Backup-block restore

Each decoded PostgreSQL block reference may carry a backup block / full-page
image. Stage 1 supports only the uncompressed forms:

- plain 8 KiB image,
- image with a zero hole (`hole_offset` + inferred hole length).

Compressed backup images are rejected explicitly.

Restore semantics mirror upstream `RestoreBlockImage`:

1. rebuild the 8 KiB page bytes,
2. write the page to the target `(rel, blk)`, extending the relation when the
   target block is exactly `nblocks`,
3. stamp `pd_lsn = record.EndLSN` after restore,
4. respect idempotency by skipping when the existing page already has
   `pd_lsn >= record.EndLSN`.

This replay kernel is reusable for later PostgreSQL redo kinds.

### 4. Heap insert replay

Implement `RM_HEAP_ID / XLOG_HEAP_INSERT` because Scenario A's write workload is
plain `INSERT INTO public.bench_log ...` and the table has no index.

Stage 1 handles two cases:

1. **backup-image case**: block 0 carries an applyable full-page image. Replay
   restores the image and does nothing else.
2. **logical insert-on-existing-page case**: no applied image. Replay decodes:
   - main data: `xl_heap_insert { offnum, flags }`
   - block 0 data: `xl_heap_header + tuple data`

The tuple bytes are reconstructed into goopg's heap-tuple representation using
the upstream-compatible page/tuple layout already used by `internal/storage`:

- `xmin = xl_xid` from the enclosing `XLogRecord` header,
- `xmax = 0`, `xvac = 0`,
- `ctid = (blk, offnum)`,
- `infomask2`, `infomask`, `hoff` from `xl_heap_header`,
- tuple body bytes copied verbatim.

Replay then inserts the raw tuple at `offnum` using the existing page helpers
instead of inventing a parallel page-mutator.

### 5. Transaction markers for standby visibility

`RM_XACT_ID` commit / abort records remain physical no-ops, but the standby
stream replayer must surface them to the existing MVCC replay hook so tuples
inserted by PostgreSQL WAL become visible to standby readers.

Stage 1 therefore treats:

- `XLOG_XACT_COMMIT`
- `XLOG_XACT_ABORT`

as recognised markers keyed by `xl_xid` from the record header. The physical
apply path returns `applied=false`; the streaming replayer forwards the xid to
the already-existing commit/abort hook.

### 6. Recognised no-ops vs explicit unsupported errors

To keep failure modes sharp, Stage 1 splits PostgreSQL records into:

- **recognised no-op**: `RM_XLOG_ID` control records, `RM_XACT_ID`
  commit/abort,
- **implemented**: backup-image restore, `RM_HEAP_ID / XLOG_HEAP_INSERT`,
- **unsupported**: every other PostgreSQL rmgr/opcode pair.

Unsupported records return an error that includes:

- rmgr,
- opcode (`xl_info & XLR_RMGR_INFO_MASK`),
- start / end LSN.

This replaces the current misleading native errors with an exact next blocker.

## Files to modify

| File | Change |
| --- | --- |
| `internal/wal/reader.go` | preserve canonical decoded XLog records in the page-aware read path |
| `internal/wal/iterator.go` | same for streaming iteration |
| `internal/wal/format.go` | add structured `XLogRecord` decode path instead of main-data-only collapse |
| `internal/wal/recovery.go` | replay decoded XLog block images and `RM_HEAP_ID / XLOG_HEAP_INSERT` through the unified apply path |
| `internal/wal/stream_replayer.go` | surface decoded `RM_XACT_ID` commit/abort markers to the existing MVCC replay hook |
| `internal/wal/*_test.go` | focused decoder + replay tests |

## Verification

Focused verification for this slice:

```bash
go test -count=1 ./internal/wal
```

If the heterogeneous E2E test still does not pass after this slice, the new
error should identify the next unsupported PostgreSQL WAL rmgr/opcode directly,
instead of failing through the native goopg payload decoder.
