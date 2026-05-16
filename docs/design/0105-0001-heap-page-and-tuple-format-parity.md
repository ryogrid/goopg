# 0105-0001 — Heap Page and Tuple Format Parity with PostgreSQL 18

**Status:** accepted
**Date:** 2026-05-16
**Milestone:** M0105
**Upstream reference:** `postgres/src/include/storage/bufpage.h`, `postgres/src/include/storage/itemid.h`, `postgres/src/include/storage/itemptr.h`, `postgres/src/include/access/htup_details.h`, `postgres/src/include/access/htup.h`.

## Problem

M0102-0007 landed wire-level and checkpoint interop for goopg→PG physical
replication (BASE_BACKUP, pg_control, WAL naming, CheckPoint struct encoding).
PG standby reaches `entering standby mode` but then crashes with a segfault
while attempting to read goopg's on-disk pages.

Root cause: goopg's heap page header, line-pointer encoding, and heap tuple
header were designed independently and may diverge from PG18 at the byte level.
PG's startup code reads catalog bootstrap pages using its own struct layouts;
any mismatch causes PG to interpret bytes as garbage pointers or invalid state.

## Goal

Audit goopg's on-disk page/tuple format against PG18 headers and apply the
minimum set of changes so that a PG18 process can:

1. Read goopg's `global/pg_control` (already done — M0095-0001)
2. Read goopg's catalog bootstrap pages (base/1/*) without crashing
3. Read goopg's heap pages (user tables) correctly
4. Complete startup and enter streaming standby mode

## Design

### 1. PageHeaderData (24 bytes)

**PG18 layout** (`postgres/src/include/storage/bufpage.h`):

| Offset | Size | Field | PG type |
|--------|------|-------|---------|
| 0 | 8 | `pd_lsn` | `PageXLogRecPtr` (uint64) |
| 8 | 2 | `pd_checksum` | `uint16` |
| 10 | 2 | `pd_flags` | `uint16` |
| 12 | 2 | `pd_lower` | `LocationIndex` (uint16) |
| 14 | 2 | `pd_upper` | `LocationIndex` (uint16) |
| 16 | 2 | `pd_special` | `LocationIndex` (uint16) |
| 18 | 2 | `pd_pagesize_version` | `uint16` |
| 20 | 4 | `pd_prune_xid` | `TransactionId` (uint32) |

**Goopg layout** (`internal/storage/page.go:SizeOfPageHeaderData = 24`):

Matches PG18 field-by-field. Same offsets, same sizes, same LE encoding.
`PageInit` sets `pd_lower = SizeOfPageHeaderData` and `pd_upper = pd_special`.

- `pd_pagesize_version` packs `BlockSize` and `pgPageLayoutVersion` — need
  to verify the packed value matches PG18 (`(BlockSize) | (version << 16)` … or
  `(version << 16) | BlockSize`).
- `pd_flags` bit definitions must match PG18: `PD_HAS_FREE_LINES = 0x0001`,
  `PD_PAGE_FULL = 0x0002`, `PD_ALL_VISIBLE = 0x0004`.
- `pd_prune_xid` — set correctly during `PageSetHeapTupleXmax` etc.

**Action:** Verify `pd_pagesize_version` encoding matches PG18. Confirm
`pd_flags` values and that `pd_prune_xid` is written correctly.

### 2. ItemIdData (4 bytes)

**PG18 layout** (`postgres/src/include/storage/itemid.h`):

```c
typedef struct ItemIdData {
    unsigned lp_off:15,    // offset to tuple (from start of page)
             lp_flags:2,   // state of line pointer
             lp_len:15;    // byte length of tuple
} ItemIdData;
```

Bit layout (32-bit LE word):
| Bits | Field |
|------|-------|
| 0-14 | `lp_off` (15 bits) |
| 15-16 | `lp_flags` (2 bits) |
| 17-31 | `lp_len` (15 bits) |

Flag values: `LP_UNUSED=0`, `LP_NORMAL=1`, `LP_REDIRECT=2`, `LP_DEAD=3`.

**Goopg layout** (`internal/storage/heap.go`):

```go
func (i ItemID) pack() (uint32, error) {
    raw := uint32(i.Offset&0x7FFF) |
           (uint32(i.Flags&0x3) << 15) |
           (uint32(i.Length&0x7FFF) << 17)
}
```

This matches PG18 exactly. `Offset=lp_off`, `Flags=lp_flags`, `Length=lp_len`.
Unpack mirrors with the same bitmasks.

**Action:** Confirm `ItemIDUnused=0`, `ItemIDNormal=1`, `ItemIDRedirect=2`,
`ItemIDDead=3` match PG18 values. No code changes expected.

### 3. HeapTupleHeaderData (23 bytes)

**PG18 layout** (`postgres/src/include/access/htup_details.h`):

| Offset | Size | Field | PG type |
|--------|------|-------|---------|
| 0 | 4 | `t_xmin` | `TransactionId` (uint32) |
| 4 | 4 | `t_xmax` | `TransactionId` (uint32) |
| 8 | 4 | `t_field3` | union of `CommandId`/`TransactionId` (uint32) |
| 12 | 4 | `t_ctid.ip_blkid` | `BlockNumber` (uint32) |
| 16 | 2 | `t_ctid.ip_posid` | `OffsetNumber` (uint16) |
| 18 | 2 | `t_infomask2` | `uint16` |
| 20 | 2 | `t_infomask` | `uint16` |
| 22 | 1 | `t_hoff` | `uint8` |

`SizeofHeapTupleHeader = offsetof(HeapTupleHeaderData, t_bits) = 23`.

**Goopg layout** (`internal/storage/heap.go`):

```go
binary.LittleEndian.PutUint32(out[0:4],  uint32(t.Header.Xmin))      // xmin
binary.LittleEndian.PutUint32(out[4:8],  uint32(t.Header.Xmax))      // xmax
binary.LittleEndian.PutUint32(out[8:12], uint32(t.Header.Xvac))      // t_field3
binary.LittleEndian.PutUint32(out[12:16], uint32(t.Header.CTID.Block)) // ctid blk
binary.LittleEndian.PutUint16(out[16:18], t.Header.CTID.Offset)       // ctid off
binary.LittleEndian.PutUint16(out[18:20], t.Header.Infomask2)         // infomask2
binary.LittleEndian.PutUint16(out[20:22], t.Header.Infomask)          // infomask
out[22] = byte(hoff)                                                   // hoff
```

This matches PG18 **exactly**. Same byte layout, same field sizes, same order.
Note: PG's `t_infomask2` is BEFORE `t_infomask` on disk (offsets 18 and 20),
matching goopg's emitter order.

**Action:** Verify that:
- `t_hoff` (`DefaultHeapTupleHoff = 24` in goopg) matches PG's computed value
  for tuples with no null bitmap and no OID.
- Null bitmap encoding, when present (`HEAP_HASNULL`), is byte-compatible:
  bitmap bytes at `HeaderSize` through `hoff - 1` (MAXALIGN'd).
- `infomask`/`infomask2` bit definitions match PG18.

### 4. Catalog Bootstrap Pages

**Current state:** `bootstrapSystemCatalogs` and `bootstrapCLog` (in
`internal/initdb/initdb.go`) write initial catalog pages using goopg's
`PageInit` + `PageAddHeapTuple`. The resulting pages use goopg's page/tuple
format — which should already be PG-compatible per the audit above.

**Potential issues:**
- `pg_class`, `pg_attribute`, `pg_type` etc. are written as goopg tuples
  with goopg's internal column layouts. PG expects specific columns at
  specific offsets. However, PG's startup code reads these pages *structurally*
  (via index scans or seq scans), not via hardcoded offsets. If the page/tuple
  format is identical, PG should be able to iterate tuples.
- The `pg_control` file already passes `pg_controldata` validation.

**Action:** After page/tuple format fixes, test PG standby startup. If PG still
crashes on catalog pages, triage the specific crash site and add targeted fixes.

### 5. WAL Replay Compatibility

Goopg's WAL writer now emits PG-compatible `XLogRecord` frames. PG's startup
replay reads the checkpoint record (88-byte CheckPoint) and replay subsequent
records. The CheckPoint already encodes valid `nextXid`/`nextOid`/etc.

**Remaining WAL interop concerns:**
- Goopg's WAL records use internal `RecordKind*` payloads wrapped in PG
  `XLogRecord` headers. PG's recovery reader skips records it doesn't understand.
- The checkpoint record (Info=0x10) is recognised by PG.
- Heap insert records from `CREATE TABLE` etc. use goopg's internal encoding.
  PG may skip or error on these. Since the test creates a table before
  `pg_basebackup`, PG replays WAL from that point.

**Action:** After page/tuple format fixes, verify PG's WAL replay doesn't
crash. Use `pg_waldump` to verify records are readable.

## Files to Modify

| File | Change |
| --- | --- |
| `internal/storage/page.go` | Verify `pd_pagesize_version`, `pd_flags`, `InitPage` encoding |
| `internal/storage/heap.go` | Verify infomask bits, `DefaultHeapTupleHoff`, null bitmap encoding |
| `internal/initdb/initdb.go` | Possibly add PG-required subdirectories or meta-files |
| `internal/storage/page_test.go` | Add cross-check tests against PG byte dumps |
| `internal/storage/heap_test.go` | Add cross-check tests against PG byte dumps |

## Verification

Focused verification for each sub-milestone:

```bash
# Format parity unit tests
go test -count=1 ./internal/storage/

# WAL replay
go test -count=1 ./internal/wal/

# M0102 Scenario B E2E
GOOPG_RUN_BLOCKED_M0102_E2E=1 go test -v -run TestE2E_FailoverGoopgToPG -timeout 15m ./internal/testport/
```

## References

- `postgres/src/include/storage/bufpage.h` — PageHeaderData, pd_flags, PageInit
- `postgres/src/include/storage/itemid.h` — ItemIdData bit-field layout
- `postgres/src/include/access/htup_details.h` — HeapTupleHeaderData, infomask bits
- `postgres/src/include/access/htup.h` — HeapTupleHeaderData macros
- `postgres/src/include/storage/itemptr.h` — ItemPointerData
- `postgres/src/backend/storage/page/bufpage.c` — PageInit implementation
