package catalog

import (
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestBuildCanonicalHeapInsertPayload verifies the byte layout of a canonical
// XLOG_HEAP_INSERT payload, including the goopg envelope header and the
// PG-canonical block-reference + main-data body.
func TestBuildCanonicalHeapInsertPayload(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 5, RelOid: 1259, Fork: storage.MainFork}
	blk := storage.BlockNumber(0)
	page := make(storage.Page, storage.BlockSize)
	// Fill page with a recognisable pattern to verify the FPI copy.
	for i := range page {
		page[i] = byte(i % 251)
	}
	offnum := uint16(1)
	xid := uint32(42)

	payload := BuildCanonicalHeapInsertPayload(rel, blk, page, offnum, xid)

	// ---- Goopg canonical envelope (7 bytes) ----
	if payload[0] != RecordKindCanonical {
		t.Fatalf("payload[0] = 0x%02x, want RecordKindCanonical (0x%02x)", payload[0], RecordKindCanonical)
	}
	if payload[1] != canonicalRmgrHeap {
		t.Fatalf("payload[1] (rmgr) = %d, want %d (RmgrHeap)", payload[1], canonicalRmgrHeap)
	}
	if payload[2] != canonicalInfoHeapInsert {
		t.Fatalf("payload[2] (info) = 0x%02x, want 0x%02x (XLOG_HEAP_INSERT)", payload[2], canonicalInfoHeapInsert)
	}
	gotXID := binary.LittleEndian.Uint32(payload[3:7])
	if gotXID != xid {
		t.Fatalf("payload[3:7] (xid) = %d, want %d", gotXID, xid)
	}

	// ---- PG-canonical body starts at byte 7 ----
	// Expected total: 7 (envelope) + 25 (block ref) + 8192 (FPI) + 5 (main data) = 8229 bytes.
	const wantLen = canonicalHeaderSize + 25 + storage.BlockSize + 5
	if len(payload) != wantLen {
		t.Fatalf("len(payload) = %d, want %d", len(payload), wantLen)
	}

	body := payload[canonicalHeaderSize:]

	// Block reference header.
	if body[0] != 0 {
		t.Errorf("block ID = %d, want 0", body[0])
	}
	wantForkFlags := canonicalBkpBlockHasImage
	if body[1] != wantForkFlags {
		t.Errorf("forkFlags = 0x%02x, want 0x%02x", body[1], wantForkFlags)
	}
	dataLen := binary.LittleEndian.Uint16(body[2:4])
	if dataLen != 0 {
		t.Errorf("data_len = %d, want 0", dataLen)
	}

	// Block image header.
	imgLen := binary.LittleEndian.Uint16(body[4:6])
	if imgLen != storage.BlockSize {
		t.Errorf("imgLen = %d, want %d", imgLen, storage.BlockSize)
	}
	holeOff := binary.LittleEndian.Uint16(body[6:8])
	if holeOff != 0 {
		t.Errorf("holeOffset = %d, want 0", holeOff)
	}
	if body[8] != canonicalBkpImageApply {
		t.Errorf("bimgInfo = 0x%02x, want bkpImageApply (0x%02x)", body[8], canonicalBkpImageApply)
	}

	// RelFileLocator.
	spc := binary.LittleEndian.Uint32(body[9:13])
	if spc != canonicalDefaultTablespaceOID {
		t.Errorf("spcOID = %d, want %d", spc, canonicalDefaultTablespaceOID)
	}
	db := binary.LittleEndian.Uint32(body[13:17])
	if db != rel.DBOid {
		t.Errorf("dbOID = %d, want %d", db, rel.DBOid)
	}
	relOid := binary.LittleEndian.Uint32(body[17:21])
	if relOid != rel.RelOid {
		t.Errorf("relOID = %d, want %d", relOid, rel.RelOid)
	}

	// Block number.
	bn := binary.LittleEndian.Uint32(body[21:25])
	if bn != uint32(blk) {
		t.Errorf("blockNum = %d, want %d", bn, blk)
	}

	// FPI bytes.
	fpi := body[25 : 25+storage.BlockSize]
	for i, b := range fpi {
		if b != page[i] {
			t.Fatalf("FPI mismatch at byte %d: got 0x%02x, want 0x%02x", i, b, page[i])
		}
	}

	// Main data.
	md := body[25+storage.BlockSize:]
	if len(md) != 5 {
		t.Fatalf("main data len = %d, want 5", len(md))
	}
	if md[0] != canonicalXlogDataShort {
		t.Errorf("main data tag = 0x%02x, want xlrBlockIDDataShort (0x%02x)", md[0], canonicalXlogDataShort)
	}
	if md[1] != 3 {
		t.Errorf("main data length byte = %d, want 3", md[1])
	}
	gotOffnum := binary.LittleEndian.Uint16(md[2:4])
	if gotOffnum != offnum {
		t.Errorf("main data offnum = %d, want %d", gotOffnum, offnum)
	}
	if md[4] != 0 {
		t.Errorf("main data flags = 0x%02x, want 0", md[4])
	}
}

// TestBuildCanonicalBtreeInsertPayload verifies the XLOG_BTREE_INSERT_LEAF
// payload layout.
func TestBuildCanonicalBtreeInsertPayload(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 5, RelOid: 2662, Fork: storage.MainFork}
	blk := storage.BlockNumber(1)
	page := make(storage.Page, storage.BlockSize)
	offnum := uint16(3)
	xid := uint32(7)

	payload := BuildCanonicalBtreeInsertPayload(rel, blk, page, offnum, xid)

	if payload[0] != RecordKindCanonical {
		t.Fatalf("payload[0] = 0x%02x, want RecordKindCanonical", payload[0])
	}
	if payload[1] != canonicalRmgrBtree {
		t.Fatalf("payload[1] (rmgr) = %d, want %d (RmgrBtree)", payload[1], canonicalRmgrBtree)
	}
	if payload[2] != canonicalInfoBtreeInsert {
		t.Fatalf("payload[2] (info) = 0x%02x, want 0x%02x", payload[2], canonicalInfoBtreeInsert)
	}

	// Length: 7 + 25 + 8192 + 4 = 8228.
	const wantLen = canonicalHeaderSize + 25 + storage.BlockSize + 4
	if len(payload) != wantLen {
		t.Fatalf("len(payload) = %d, want %d", len(payload), wantLen)
	}

	md := payload[canonicalHeaderSize+25+storage.BlockSize:]
	if md[0] != canonicalXlogDataShort {
		t.Errorf("main data tag = 0x%02x", md[0])
	}
	if md[1] != 2 {
		t.Errorf("main data len = %d, want 2", md[1])
	}
	gotOffnum := binary.LittleEndian.Uint16(md[2:4])
	if gotOffnum != offnum {
		t.Errorf("offnum = %d, want %d", gotOffnum, offnum)
	}
}

// TestPgCanonicalHeapInsert_NilLogFn verifies that a nil logFn is a no-op.
func TestPgCanonicalHeapInsert_NilLogFn(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 5, RelOid: 1259}
	page := make(storage.Page, storage.BlockSize)
	if err := PgCanonicalHeapInsert(rel, 0, page, 1, 42, nil); err != nil {
		t.Fatalf("unexpected error with nil logFn: %v", err)
	}
}

// TestRelationMapUpdateMap_Stub verifies that the stub returns nil.
func TestRelationMapUpdateMap_Stub(t *testing.T) {
	if err := RelationMapUpdateMap(5, 1259, 1259, false, nil); err != nil {
		t.Fatalf("RelationMapUpdateMap stub returned error: %v", err)
	}
}
