# 0014-0002 — XLogRecord Header + Rmgr Mapping

**Status:** accepted (step 1)
**Milestone:** [0014 — PostgreSQL-Compatible WAL On-Disk Format](../../milestones/0014-wal-compatibility-with-pg.md)
**Spans seam:** XLogRecord header layout, CRC32C algorithm,
RmgrId vocabulary, mapping for goopg's existing record kinds.
**Cross-links:**
[0014-0001](0014-0001-xlog-page-and-segment-layout-compat.md) (page-header
foundation; this slice's records sit inside those pages),
[0007-0001](0007-0001-wal-segment-preallocation.md) (the legacy
length+CRC frame this slice replaces),
[0002-0003](0002-0003-redo-records.md) (current goopg record kinds —
heap insert/delete/vacuum, btree insert/split, page-image, checkpoint).

## Context

goopg currently frames each WAL record as `[len:uint32][crc:uint32(IEEE)][payload]`
— an 8-byte header with `crc32.IEEE` checksum. PostgreSQL uses a
24-byte `XLogRecord` header with CRC32C (Castagnoli polynomial) and
chains records via `xl_prev` (the previous record's start LSN). To
reach pg_waldump compatibility (M0014-0003 acceptance gate), goopg
must adopt the same header shape and CRC algorithm.

This **step 1** is purely additive — types, constants, encode/decode
helpers, and a CRC32C function — with no production-path changes
yet. The actual writer/reader switchover for both M0014-0001
(per-page headers) and M0014-0002 (XLogRecord frames) lands together
in a later loop when the format flag flips atomically; doing them in
one switchover keeps the on-disk format coherent — a half-migrated
segment file with new pages but legacy records would be neither
upstream-compatible nor decodable by goopg's legacy reader.

## XLogRecord layout

Mirrors `postgres/src/include/access/xlogrecord.h`:

```go
type XLogRecord struct {
    TotLen uint32  // xl_tot_len — total bytes including header
    XID    uint32  // xl_xid     — xact id (TransactionId is uint32)
    Prev   uint64  // xl_prev    — start LSN of the previous record
    Info   uint8   // xl_info    — flag bits (XLR_*)
    Rmid   uint8   // xl_rmid    — resource manager id
    // 2 bytes of padding here, MUST be zeroed (upstream initialises them)
    CRC    uint32  // xl_crc     — CRC32C over header + payload, with xl_crc field treated as zero
}
```

`SizeOfXLogRecord = 24` (4 + 4 + 8 + 1 + 1 + 2 + 4).

### Endianness

Little-endian on disk, matching upstream's de-facto host-byte-order
convention on x86_64 / aarch64 (see 0014-0001 for the
cross-architecture-out-of-scope rationale).

### Flag bits

```go
const (
    XLRInfoMask         uint8 = 0x0F  // low 4 bits: framework-set
    XLRRmgrInfoMask     uint8 = 0xF0  // high 4 bits: rmgr-set
    XLRSpecialRelUpdate uint8 = 0x01
    XLRCheckConsistency uint8 = 0x02
)
```

## RmgrId vocabulary

Mirrors `postgres/src/include/access/rmgrlist.h`. Only the IDs goopg's
current record kinds map to are defined; the rest land when their
producing paths exist (e.g. `RM_GIN_ID` only matters once goopg has
GIN indexes).

```go
const (
    RmgrXLog        Rmgr = 0  // RM_XLOG_ID — checkpoints, EOL markers, switch
    RmgrXact        Rmgr = 1  // RM_XACT_ID — commit/abort
    RmgrStorage     Rmgr = 2  // RM_SMGR_ID — relation create/truncate
    RmgrCLOG        Rmgr = 3  // (deferred — goopg uses synchronous commit only)
    // 4..8 deferred similarly
    RmgrHeap2       Rmgr = 9  // RM_HEAP2_ID — heap multi-insert / vacuum
    RmgrHeap        Rmgr = 10 // RM_HEAP_ID  — heap insert/delete/update
    RmgrBtree       Rmgr = 11 // RM_BTREE_ID — btree insert/split
    // RmgrHash..RmgrGeneric fill upstream's slots; goopg defines
    // them as named constants only when a producer lands.
)
```

The numeric values match upstream so `pg_waldump --rmgr=Heap` filters
work against goopg-emitted WAL out of the box.

## Goopg record-kind → rmgr/info mapping

Defined here for the M0014-0001+0002 switchover slice; not yet
applied to live records. The current `RecordKind*` values
(internal/wal/recovery.go) map as follows:

| goopg RecordKind                  | Rmgr      | Info (high 4 bits)    | Notes                                   |
|-----------------------------------|-----------|------------------------|-----------------------------------------|
| RecordKindHeapInsert              | RmgrHeap  | XLOG_HEAP_INSERT (0x00)  | Existing tuple-insert payload mostly maps. |
| RecordKindHeapDelete              | RmgrHeap  | XLOG_HEAP_DELETE (0x10)  |                                         |
| RecordKindBtreeInsert             | RmgrBtree | XLOG_BTREE_INSERT_LEAF (0x00) |                                         |
| RecordKindBtreeSplit              | RmgrBtree | XLOG_BTREE_SPLIT_L (0x20)     | (or _R variant if right-side split)     |
| RecordKindVacuum (heap)           | RmgrHeap2 | XLOG_HEAP2_PRUNE (0x10)       |                                         |
| RecordKindCheckpointShutdown      | RmgrXLog  | XLOG_CHECKPOINT_SHUTDOWN (0x00) |                                       |
| RecordKindCheckpointOnline        | RmgrXLog  | XLOG_CHECKPOINT_ONLINE (0x10)   |                                       |
| RecordKindXactCommit              | RmgrXact  | XLOG_XACT_COMMIT (0x00)         |                                       |
| RecordKindXactAbort               | RmgrXact  | XLOG_XACT_ABORT (0x20)          |                                       |
| RecordKindPageImage (FPI)         | (encoded as block-header backup-block, not a separate kind upstream) |  | Actual switchover slice will refactor PageImage records into XLogRecordBlockHeader form. |

The "Info" values are placeholders for the switchover slice —
upstream defines them in `heapam_xlog.h` / `nbtxlog.h` /
`xact.h`. This slice doesn't define them yet (the constants land
alongside the producer changes); the table is the contract the
switchover follows.

## CRC32C

Castagnoli polynomial 0x1EDC6F41. Go provides
`crc32.MakeTable(crc32.Castagnoli)` and `crc32.Update`. Upstream's
`COMP_CRC32C` initialises the running CRC to 0xFFFFFFFF, updates
across the bytes, then finalises by XOR with 0xFFFFFFFF — same as
Go's `crc32.New`/`Sum32`/`Update` semantics. The encoded `xl_crc`
field is computed with the field bytes (offset 20..23 of the header)
treated as zero during the running checksum, matching upstream's
`FIN_CRC32C(rdata->crc)` after reset of those 4 bytes.

Helper:

```go
// XLogCRC32C returns the upstream-compatible checksum over `data`.
// The caller is responsible for assembling the full byte slice
// (header + payload) with the xl_crc field zeroed.
func XLogCRC32C(data []byte) uint32
```

## Encode / decode

```go
// EncodeXLogRecordHeader writes a 24-byte XLogRecord header to
// dst[:SizeOfXLogRecord]. Computes xl_crc over (header || payload)
// with the xl_crc field treated as zero.
func EncodeXLogRecordHeader(dst []byte, h XLogRecord, payload []byte) error

// DecodeXLogRecordHeader parses a 24-byte XLogRecord header. Does
// NOT validate xl_crc — the caller (recovery / pg_waldump-style
// readers) must invoke XLogCRC32C over the full record bytes and
// compare. Returns ErrInvalidRecordHeader on undefined Info bits
// or unrecognised Rmgr.
func DecodeXLogRecordHeader(src []byte) (XLogRecord, error)
```

The header carries 24 bytes — the payload follows it on disk
without further padding. Payloads with rmgr-specific block-data
formatting (XLogRecordBlockHeaders, FPI back-up blocks) are the
producer's responsibility; this slice doesn't decompose them.

## Tests (this slice)

- `TestXLogRecordHeaderRoundTrip` — encode / decode preserves
  every field including Prev (uint64) and Rmid.
- `TestXLogRecordCRCMatchesUpstream` — fixed payload `[]byte("hello")`
  against a fixed (TotLen=29, XID=0, Prev=0, Info=0, Rmid=0)
  header produces a deterministic CRC32C value. Pins the algorithm;
  any drift surfaces immediately. Expected value computed against
  Go's `crc32.MakeTable(crc32.Castagnoli)` over the assembled
  byte slice with xl_crc zeroed.
- `TestXLogCRC32CIsCastagnoli` — CRC32C of `[]byte("123456789")`
  is `0xE3069283` (the canonical Castagnoli test vector).
- `TestEncodeXLogRecordRejectsUndefinedInfoBits` — rmgr-area bits
  (high 4) and framework-area bits (low 4) coexist in `Info`;
  setting any of the two reserved bits in the framework area
  beyond `XLRInfoMask` is fine but writing past byte 0 is not.
  Test pins the layout: padding bytes (offsets 18..19) MUST be
  zero on disk.
- `TestDecodeXLogRecordHeaderTruncatedSrc` — short buffer returns
  the truncation error rather than reading past slice bounds.

## Out of scope

- Actual writer integration (consume CRC and xl_prev chaining in
  `state.append`) — joint switchover with M0014-0001 step 2.
- XLogRecordBlockHeader / XLogRecordDataHeader payload framing —
  separate slice.
- Per-rmgr decoders (heap/btree/xlog descriptions) — those land
  as the producer code is refactored, one rmgr at a time.
- pg_waldump compatibility validation — M0014-0003 acceptance gate.
- Backwards-compatibility with the legacy 8-byte length+CRC32-IEEE
  frame — M0014-0004 (the legacy-format detector branches on
  ErrInvalidPageHeader from M0014-0001 step 1).
