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
type LogCanonicalFunc func(payload []byte) error

// PG resource manager IDs mirrored from internal/wal/xlog_record.go.
// Defined locally to avoid the catalog → wal import cycle.
const (
	canonicalRmgrHeap  uint8 = 10 // RM_HEAP_ID
	canonicalRmgrBtree uint8 = 11 // RM_BTREE_ID
)

// PG info byte opcodes for catalog WAL records.
const (
	canonicalInfoHeapInsert  uint8 = 0x00 // XLOG_HEAP_INSERT
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
) error {
	if logFn == nil {
		return nil
	}
	payload := BuildCanonicalHeapInsertPayload(rel, blk, page, offnum, xid)
	return logFn(payload)
}

// BuildCanonicalHeapInsertPayload builds the full canonical payload for
// PgCanonicalHeapInsert. Exposed for unit testing.
func BuildCanonicalHeapInsertPayload(
	rel storage.RelFileNode,
	blk storage.BlockNumber,
	page storage.Page,
	offnum uint16,
	xid uint32,
) []byte {
	body := buildCanonicalBlockRefFPI(rel, blk, page)

	// Main data: xlrBlockIDDataShort(1) + length(1) + offnum(2) + flags(1) = 5 bytes.
	// xl_heap_insert.flags = 0 (FPI path; XLH_INSERT_CONTAINS_NEW_TUPLE not needed).
	mainData := [5]byte{canonicalXlogDataShort, 3}
	binary.LittleEndian.PutUint16(mainData[2:4], offnum)
	mainData[4] = 0

	return buildCanonicalPayload(canonicalRmgrHeap, canonicalInfoHeapInsert, xid,
		append(body, mainData[:]...))
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
) error {
	if logFn == nil {
		return nil
	}
	payload := BuildCanonicalBtreeInsertPayload(rel, blk, page, offnum, xid)
	return logFn(payload)
}

// BuildCanonicalBtreeInsertPayload builds the full canonical payload for
// PgCanonicalBtreeInsert. Exposed for unit testing.
func BuildCanonicalBtreeInsertPayload(
	rel storage.RelFileNode,
	blk storage.BlockNumber,
	page storage.Page,
	offnum uint16,
	xid uint32,
) []byte {
	body := buildCanonicalBlockRefFPI(rel, blk, page)

	// Main data: xlrBlockIDDataShort(1) + length(1) + xl_btree_insert.offnum(2) = 4 bytes.
	// With FPI, PG's btree_xlog_insert restores the page directly and ignores offnum;
	// we still include it for correctness with non-FPI recovery paths.
	mainData := [4]byte{canonicalXlogDataShort, 2}
	binary.LittleEndian.PutUint16(mainData[2:4], offnum)

	return buildCanonicalPayload(canonicalRmgrBtree, canonicalInfoBtreeInsert, xid,
		append(body, mainData[:]...))
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
func buildCanonicalBlockRefFPI(rel storage.RelFileNode, blk storage.BlockNumber, page storage.Page) []byte {
	const blockRefSize = 25 // header(4) + image header(5) + relfilelocator(12) + blocknum(4)
	buf := make([]byte, blockRefSize+storage.BlockSize)

	// Block reference header.
	buf[0] = 0 // block ID 0
	buf[1] = canonicalBkpBlockHasImage | 0x00 // forkFlags: HasImage + MainFork
	binary.LittleEndian.PutUint16(buf[2:4], 0)                           // data_len = 0

	// Block image header.
	binary.LittleEndian.PutUint16(buf[4:6], uint16(storage.BlockSize)) // imgLen
	binary.LittleEndian.PutUint16(buf[6:8], 0)                         // holeOffset
	buf[8] = canonicalBkpImageApply                                     // bimgInfo

	// RelFileLocator.
	binary.LittleEndian.PutUint32(buf[9:13], canonicalDefaultTablespaceOID)
	binary.LittleEndian.PutUint32(buf[13:17], rel.DBOid)
	binary.LittleEndian.PutUint32(buf[17:21], rel.RelOid)

	// Block number.
	binary.LittleEndian.PutUint32(buf[21:25], uint32(blk))

	// Full page image.
	copy(buf[25:], page)

	return buf
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
