# 0014-0003 — Recovery, Streaming, and Compatibility Validation

**Status:** accepted
**Milestone:** [0014 — PostgreSQL-Compatible WAL On-Disk Format](../milestones/0014-wal-compatibility-with-pg.md)
**Spans seam:** writer record-frame switchover, reader/iterator/recovery decode updates, pg_waldump validation.
**Cross-links:**
[0014-0001](0014-0001-xlog-page-and-segment-layout-compat.md),
[0014-0002](0014-0002-xlogrecord-header-and-rmgr-mapping.md),
[0014-0004](0014-0004-rollout-guardrails-and-operator-playbook.md),
[0007-0001](0007-0001-wal-segment-preallocation.md),
[0010-0002](0010-0002-walsender-in-memory-wal-handoff.md).

## Context

M0014-0001 step 2 landed page headers but still used the legacy 8-byte
record frame (`len + crc32.IEEE`) inside those pages. That shape is not
accepted by upstream readers. M0014-0002 step 1 already provided
`XLogRecord` helpers and CRC32C primitives; this slice wires them into
live WAL paths and updates every decode/replay/streaming reader to match.

## Decisions

### 1. Page-header mode now uses XLogRecord frames

When `wal.Config.PageHeaders=true`, `state.append` no longer calls the
legacy record encoder. It emits:

1. `XLogRecord` header (24 bytes, CRC32C, `xl_prev` chaining)
2. A valid PostgreSQL record-data chunk for goopg's payload bytes
   (`XLR_BLOCK_ID_DATA_SHORT` for payload <= 255 bytes,
   `XLR_BLOCK_ID_DATA_LONG` otherwise)
3. Existing goopg payload bytes unchanged as the chunk body

Legacy mode (`PageHeaders=false`) remains byte-identical.

### 2. Minimal valid XLog payload structure

goopg does not yet emit upstream block-reference substructures for every
rmgr operation. To make records parseable by generic upstream readers now,
this slice wraps goopg payloads as a single main-data chunk. Decode paths
unwrap that chunk and hand the original payload back to existing replay
and logical-classifier code.

This yields:

- pg_waldump parseability today
- no redo-payload rewrite in this slice
- stable replay semantics for existing `RecordKind*` decoders

### 3. xl_prev persistence across restart

`state` now tracks the previous record's upstream-style 0-based RecPtr
(`prevRecPtr = start - 1`, since goopg's `start` LSN is 1-based) and
stamps it into each new XLogRecord header. The on-wire xl_prev value
is therefore byte-identical to what upstream PG emits, so
pg_waldump's prev-link validation passes.

Writer startup scan (`detectWritePos` / `scanLastSegmentEnd`) now returns:

- end-of-stream write position
- start LSN of the last complete record

So `xl_prev` chaining continues correctly after process restart.

### 5. Upstream byte-compat fixes (M0014-0003 finalisation)

Empirical pg_waldump validation surfaced three encoder bugs that this
slice corrects (`TestPGWaldumpParsesEmittedWAL` is the gate):

1. `XLOGPageMagic` was `0xD119` but PG18.3 ships `0xD118`
   (postgres/src/include/access/xlog_internal.h). Pinning to the
   release in `local_install/`.
2. xl_crc was computed over `(header[0..19] || payload)` but upstream's
   `XLogInsertRecord` (postgres/src/backend/access/transam/xlog.c
   ~line 5170) computes over `(payload || header[0..19])` — CRC32C is
   order-dependent. `EncodeXLogRecordHeader` and `VerifyXLogRecordCRC`
   now match the upstream order.
3. Records were packed back-to-back without MAXALIGN(8) trailing pad.
   pg_waldump's nextRecord advance is `MAXALIGN(xl_tot_len)` and
   landed in the middle of the next record, surfacing as
   "invalid resource manager ID". The encoder now appends zero pad
   bytes out to MAXALIGN; xl_tot_len is unchanged (pad is not
   counted, matching upstream); xlp_rem_len for cross-page records
   uses `realRecLen` (xl_tot_len) so contrecord pages don't claim the
   pad as record content. Reader/iterator/scanner all advance by
   MAXALIGN.

### 4. Decode path updates are centralized and mode-aware

Added XLog helpers in `internal/wal/format.go`:

- `encodeRecordXLog(payload, prev)`
- `decodeRecordXLog(stream)`
- wrapper/unwrap helpers for main-data chunk tags

Call-site behavior:

- `ReadAll`: legacy path unchanged; page-aware path decodes XLogRecord
- `RecordIterator`: legacy path unchanged; page-header mode decodes XLogRecord
- startup segment scan: legacy path unchanged; page-header mode validates XLogRecord

## Recovery and Streaming impact

No API surface changes in higher layers:

- `Record.Payload` remains goopg payload bytes
- replay functions (`ApplyRecord`, `ReplayFromDir*`) are unchanged and still
  operate on existing `RecordKind*` payload formats
- walsender/walreceiver continue to exchange payloads, not raw segment bytes;
  ordering behavior is unchanged

The only on-disk/wire-format difference is in page-header mode WAL bytes.

## Validation

### Unit and integration coverage added

- `format_xlog_test.go`
  - XLog encode/decode round-trip (short and long data chunks)
  - malformed chunk rejection
  - xid/rmgr classification sanity
- `xlog_emit_test.go`
  - updated `xlp_rem_len` arithmetic for XLogRecord framing overhead
  - new `xl_prev` chain test (`TestPageEmissionXLogPrevChain`)
- `recovery_test.go`
  - new end-to-end recovery test with `PageHeaders=true`

### pg_waldump acceptance gate

`pg_waldump_compat_test.go` adds `TestPGWaldumpParsesEmittedWAL`:

- emits representative WAL records under `PageHeaders=true`
- executes upstream `pg_waldump -q` against emitted segment(s)
- fails on parser errors
- skips only when pg_waldump binary is unavailable

This is the M0014-0003 compatibility gate.

## Out of scope

- Segment filename switchover to `XLogFileName` in live writer paths
- Full upstream rmgr block-reference encoding for all goopg record kinds
- Rollout default-on/fail-fast policy (M0014-0004 step 2)
- Runtime SQL observability of active WAL format mode
