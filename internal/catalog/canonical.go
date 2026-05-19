package catalog

import (
	"encoding/binary"

	"github.com/goopg/goopg/internal/storage"
)

// LogCanonicalFunc is the callback for emitting PG-canonical WAL records.
// The executor/initdb layer wires it to walWriter.Append(payload), where
// payload begins with RecordKindCanonical (0xFE) followed by the rmgr/info/xid
// and the PG-canonical XLogRecord body (block references + main data).
// Using a callback avoids the import cycle: wal → catalog → wal.
//
// The returned uint64 is the WAL end-LSN of the appended record (caller can
// stamp it onto the page's pd_lsn so a PG18 standby's recovery applies FPIs
// only when its on-disk page is older than the record).
type LogCanonicalFunc func(payload []byte) (uint64, error)

// PG resource manager IDs mirrored from internal/wal/xlog_record.go.
// Defined locally to avoid the catalog → wal import cycle.
const (
	canonicalRmgrHeap  uint8 = 10 // RM_HEAP_ID
	canonicalRmgrBtree uint8 = 11 // RM_BTREE_ID
)

// PG info byte opcodes for catalog WAL records.
const (
	canonicalInfoHeapInsert  uint8 = 0x00 // XLOG_HEAP_INSERT
	canonicalInfoHeapDelete  uint8 = 0x10 // XLOG_HEAP_DELETE
	canonicalInfoBtreeInsert uint8 = 0x00 // XLOG_BTREE_INSERT_LEAF
)

// XLog block reference format flags (mirrors pg_xlog_decode.go constants).
const (
	canonicalBkpBlockHasImage uint8 = 0x10
	canonicalBkpImageApply    uint8 = 0x02
)

// XLog main-data chunk tag for short payload (≤255 bytes).
const canonicalXlogDataShort byte = 0xFF // xlrBlockIDDataShort

// Default PostgreSQL tablespace OID (pg_default = 1663).
const canonicalDefaultTablespaceOID uint32 = 1663

// RecordKindCanonical is the goopg payload kind byte used to carry a
// PG-canonical WAL record body through the WAL writer unchanged.
// Must match the constant in internal/wal/format.go.
const RecordKindCanonical byte = 0xFE

// canonicalHeaderSize is the size of the goopg-canonical payload header:
// kind(1) + rmgr(1) + info(1) + xid(4) = 7 bytes.
const canonicalHeaderSize = 7

// PgCanonicalHeapInsert encodes a PG-canonical XLOG_HEAP_INSERT WAL record
// (with a full-page image) and emits it via logFn. The FPI approach allows a
// PG18 standby to replay catalog heap insertions by simply restoring the page
// without parsing the heap-tuple internals.
//
// Parameters:
//   - rel: catalog relation's physical file node (DBOid, RelOid, Fork)
//   - blk: heap block number where the tuple was inserted
//   - page: full 8192-byte page snapshot after the insertion
//   - offnum: 1-based line-pointer slot of the inserted tuple
//   - xid: transaction ID (xmin) stamped on the tuple
//   - logFn: callback to write the encoded payload to the WAL stream
func PgCanonicalHeapInsert(
	rel storage.RelFileNode,
	blk storage.BlockNumber,
	page storage.Page,
	offnum uint16,
	xid uint32,
	logFn LogCanonicalFunc,
) (uint64, error) {
	if logFn == nil {
		return 0, nil
	}
	payload := BuildCanonicalHeapInsertPayload(rel, blk, page, offnum, xid)
	return logFn(payload)
}

// BuildCanonicalHeapInsertPayload builds the full canonical payload for
// PgCanonicalHeapInsert. Exposed for unit testing.
// BuildCanonicalHeapInsertPayload encodes a XLOG_HEAP_INSERT payload with a
// full-page image. xl_heap_insert struct layout (heapam_xlog.h):
//
//	offnum  uint16  (2 bytes)
//	flags   uint8   (1 byte) — total 3 bytes of main-data content
func BuildCanonicalHeapInsertPayload(
	rel storage.RelFileNode,
	blk storage.BlockNumber,
	page storage.Page,
	offnum uint16,
	xid uint32,
) []byte {
	// xl_heap_insert: offnum(2) + flags(1) = 3 bytes.
	// flags=0: XLH_INSERT_CONTAINS_NEW_TUPLE not needed when FPI restores the page.
	var mainData [3]byte
	binary.LittleEndian.PutUint16(mainData[0:2], offnum)
	// mainData[2] = 0 (flags)
	body := buildCanonicalSingleFPIBody(rel, blk, page, mainData[:])
	return buildCanonicalPayload(canonicalRmgrHeap, canonicalInfoHeapInsert, xid, body)
}

// PgCanonicalBtreeInsert encodes a PG-canonical XLOG_BTREE_INSERT_LEAF WAL
// record (with a full-page image) and emits it via logFn.
//
// Parameters:
//   - rel: btree index relation's physical file node
//   - blk: leaf block number where the index tuple was inserted
//   - page: full 8192-byte page snapshot after the insertion
//   - offnum: 1-based slot of the inserted index tuple on the leaf page
//   - xid: transaction ID of the inserting transaction
//   - logFn: callback to write the encoded payload to the WAL stream
func PgCanonicalBtreeInsert(
	rel storage.RelFileNode,
	blk storage.BlockNumber,
	page storage.Page,
	offnum uint16,
	xid uint32,
	logFn LogCanonicalFunc,
) (uint64, error) {
	if logFn == nil {
		return 0, nil
	}
	payload := BuildCanonicalBtreeInsertPayload(rel, blk, page, offnum, xid)
	return logFn(payload)
}

// BuildCanonicalBtreeInsertPayload builds the full canonical payload for
// PgCanonicalBtreeInsert. Exposed for unit testing.
// BuildCanonicalBtreeInsertPayload encodes a XLOG_BTREE_INSERT_LEAF payload
// with a full-page image. xl_btree_insert struct layout (nbtxlog.h):
//
//	offnum  uint16  (2 bytes) — total 2 bytes of main-data content
func BuildCanonicalBtreeInsertPayload(
	rel storage.RelFileNode,
	blk storage.BlockNumber,
	page storage.Page,
	offnum uint16,
	xid uint32,
) []byte {
	// xl_btree_insert: offnum(2) = 2 bytes.
	// With FPI, PG's btree_xlog_insert restores the page directly and ignores offnum;
	// we still include it for correctness with non-FPI recovery paths.
	var mainData [2]byte
	binary.LittleEndian.PutUint16(mainData[0:2], offnum)
	body := buildCanonicalSingleFPIBody(rel, blk, page, mainData[:])
	return buildCanonicalPayload(canonicalRmgrBtree, canonicalInfoBtreeInsert, xid, body)
}

// RelationMapUpdateMap is a stub for emitting XLOG_RELMAP_UPDATE WAL records.
// Full implementation is deferred to a future batch once the relmap serialisation
// format is implemented in goopg.
//
// Parameters match the upstream relmapper.c::RelationMapUpdateMap signature:
//   - dboid: OID of the affected database (0 for the shared map)
//   - relid: OID of the catalog relation whose mapping changes
//   - relfilenode: new physical file node OID for the relation
//   - shared: true when updating the global shared relmap
func RelationMapUpdateMap(_ uint32, _ uint32, _ uint32, _ bool, _ LogCanonicalFunc) error {
	return nil
}

// PgCanonicalHeapDelete emits a PG-canonical XLOG_HEAP_DELETE record with a
// full-page image so a vanilla PG18 standby can replay user-table DELETEs and
// the xmax-stamp step of UPDATEs. With HasImage+ImageApply the standby's
// heap_xlog_delete restores the page from the FPI directly; the xl_heap_delete
// main-data struct is still present but not used for tuple-level logic.
func PgCanonicalHeapDelete(
	rel storage.RelFileNode,
	blk storage.BlockNumber,
	page storage.Page,
	offnum uint16,
	xid uint32,
	xmax uint32,
	logFn LogCanonicalFunc,
) (uint64, error) {
	if logFn == nil {
		return 0, nil
	}
	return logFn(BuildCanonicalHeapDeletePayload(rel, blk, page, offnum, xid, xmax))
}

// BuildCanonicalHeapDeletePayload encodes a XLOG_HEAP_DELETE payload with a
// full-page image. xl_heap_delete struct layout (heapam_xlog.h):
//
//	xmax         uint32   (4 bytes)
//	offnum       uint16   (2 bytes)
//	infobits_set uint8    (1 byte)
//	flags        uint8    (1 byte) — total 8 bytes of main-data content
// BuildCanonicalHeapDeletePayload encodes a XLOG_HEAP_DELETE payload with a
// full-page image. xl_heap_delete struct layout (heapam_xlog.h):
//
//	xmax         uint32   (4 bytes)
//	offnum       uint16   (2 bytes)
//	infobits_set uint8    (1 byte)
//	flags        uint8    (1 byte) — total 8 bytes of main-data content
func BuildCanonicalHeapDeletePayload(
	rel storage.RelFileNode,
	blk storage.BlockNumber,
	page storage.Page,
	offnum uint16,
	xid uint32,
	xmax uint32,
) []byte {
	// xl_heap_delete: xmax(4) + offnum(2) + infobits_set(1) + flags(1) = 8 bytes.
	// infobits_set=0, flags=0: not used when FPI restores the page.
	var mainData [8]byte
	binary.LittleEndian.PutUint32(mainData[0:4], xmax)
	binary.LittleEndian.PutUint16(mainData[4:6], offnum)
	// mainData[6] = 0 (infobits_set)
	// mainData[7] = 0 (flags)
	body := buildCanonicalSingleFPIBody(rel, blk, page, mainData[:])
	return buildCanonicalPayload(canonicalRmgrHeap, canonicalInfoHeapDelete, xid, body)
}

// buildCanonicalBlockRefFPI encodes a single PG XLogRecord block reference
// with a full-page image (FPI) for the given relation and block.
//
// On-disk layout (all little-endian):
//
//	Block reference header (4 bytes):
//	  [0]    block ID = 0
//	  [1]    forkFlags = bkpBlockHasImage (0x10) | MainFork (0x00)
//	  [2..3] data_len = 0 (no per-block data)
//
//	Block image header (5 bytes):
//	  [4..5] imgLen = BlockSize (8192)
//	  [6..7] holeOffset = 0 (no hole; full-page image)
//	  [8]    bimgInfo = bkpImageApply (0x02)
//
//	RelFileLocator (12 bytes):
//	  [9..12]  spcOID = 1663 (default tablespace)
//	  [13..16] dbOID
//	  [17..20] relfilenode OID
//
//	Block number (4 bytes):
//	  [21..24] blockNum
//
//	Image data (8192 bytes):
//	  [25..8216] full page content
//
// Total: 25 + 8192 = 8217 bytes.
// buildCanonicalSingleFPIBody builds the PG XLogRecord body for a single
// block reference with a full-page image. The layout mirrors PG's
// XLogRecordAssemble format (xloginsert.c):
//
//	[block_ref_header(25)] [main_data_tag+len(2)] [FPI(BlockSize)] [main_data_content(N)]
//
// The block ref header and main_data_tag+len form the "header section";
// the FPI and main_data_content form the "data section". The decoder
// (parseXLogRecordData in pg_xlog_decode.go) reads them in this order:
// first all block/main-data headers, then the data section.
//
// mainDataContent must be ≤ 255 bytes (short-form main-data tag).
func buildCanonicalSingleFPIBody(
	rel storage.RelFileNode,
	blk storage.BlockNumber,
	page storage.Page,
	mainDataContent []byte,
) []byte {
	n := len(mainDataContent)
	// blockRefHdr(25) + mainDataHdr(2) + FPI(BlockSize) + mainDataContent(n)
	body := make([]byte, 25+2+storage.BlockSize+n)

	// Block reference header (4 base + 5 image + 12 locator + 4 blocknum = 25).
	body[0] = 0                               // block ID 0
	body[1] = canonicalBkpBlockHasImage | 0x00 // forkFlags: HasImage + MainFork
	binary.LittleEndian.PutUint16(body[2:4], 0) // data_len = 0
	binary.LittleEndian.PutUint16(body[4:6], uint16(storage.BlockSize)) // imgLen
	binary.LittleEndian.PutUint16(body[6:8], 0) // holeOffset = 0 (no hole)
	body[8] = canonicalBkpImageApply             // bimgInfo
	binary.LittleEndian.PutUint32(body[9:13], canonicalDefaultTablespaceOID)
	binary.LittleEndian.PutUint32(body[13:17], rel.DBOid)
	binary.LittleEndian.PutUint32(body[17:21], rel.RelOid)
	binary.LittleEndian.PutUint32(body[21:25], uint32(blk))

	// Main data header (short form): [xlrBlockIDDataShort(1)][len(1)]
	body[25] = canonicalXlogDataShort
	body[26] = byte(n)

	// Data section: FPI then main data content.
	copy(body[27:27+storage.BlockSize], page)
	copy(body[27+storage.BlockSize:], mainDataContent)

	return body
}

// buildCanonicalPayload wraps the PG-canonical record body in the goopg
// canonical envelope: kind(1) + rmgr(1) + info(1) + xid(4) + body.
// The WAL writer's classifyXLogRecord and wrapXLogMainData functions recognise
// this envelope and produce the correct XLogRecord header and body respectively.
func buildCanonicalPayload(rmgr, info uint8, xid uint32, body []byte) []byte {
	out := make([]byte, canonicalHeaderSize+len(body))
	out[0] = RecordKindCanonical
	out[1] = rmgr
	out[2] = info
	binary.LittleEndian.PutUint32(out[3:7], xid)
	copy(out[canonicalHeaderSize:], body)
	return out
}
