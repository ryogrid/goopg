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

	// Main data header comes BEFORE the FPI (correct PG XLog format):
	// body[25]=xlrBlockIDDataShort, body[26]=len(3).
	if body[25] != canonicalXlogDataShort {
		t.Errorf("body[25] (main data tag) = 0x%02x, want xlrBlockIDDataShort (0x%02x)", body[25], canonicalXlogDataShort)
	}
	if body[26] != 3 {
		t.Errorf("body[26] (main data len) = %d, want 3", body[26])
	}

	// FPI bytes are in the data section AFTER the main data header.
	fpi := body[27 : 27+storage.BlockSize]
	for i, b := range fpi {
		if b != page[i] {
			t.Fatalf("FPI mismatch at byte %d: got 0x%02x, want 0x%02x", i, b, page[i])
		}
	}

	// Main data content follows the FPI.
	mdContent := body[27+storage.BlockSize:]
	if len(mdContent) != 3 {
		t.Fatalf("main data content len = %d, want 3", len(mdContent))
	}
	gotOffnum := binary.LittleEndian.Uint16(mdContent[0:2])
	if gotOffnum != offnum {
		t.Errorf("main data offnum = %d, want %d", gotOffnum, offnum)
	}
	if mdContent[2] != 0 {
		t.Errorf("main data flags = 0x%02x, want 0", mdContent[2])
	}
}

// TestBuildCanonicalHeapPrunePayload verifies the byte layout of a canonical
// XLOG_HEAP2_PRUNE_* payload — RM_HEAP2_ID rmgr (distinct from RM_HEAP_ID),
// the onAccess/VACUUM info-byte + reason-byte selection, and that no
// block-data sub-records are encoded (FPI-only, as PG's
// heap_xlog_prune_freeze skips them entirely on a restored full-page image).
func TestBuildCanonicalHeapPrunePayload(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 5, RelOid: 16400, Fork: storage.MainFork}
	blk := storage.BlockNumber(3)
	page := make(storage.Page, storage.BlockSize)
	for i := range page {
		page[i] = byte(i % 251)
	}
	xid := uint32(777)

	cases := []struct {
		name       string
		onAccess   bool
		wantInfo   uint8
		wantReason uint8
	}{
		{"on-access", true, canonicalInfoHeap2PruneOnAccess, pruneReasonOnAccess},
		{"vacuum-scan", false, canonicalInfoHeap2PruneVacuumScan, pruneReasonVacuumScan},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := BuildCanonicalHeapPrunePayload(rel, blk, page, xid, tc.onAccess)

			if payload[0] != RecordKindCanonical {
				t.Fatalf("payload[0] = 0x%02x, want RecordKindCanonical", payload[0])
			}
			if payload[1] != canonicalRmgrHeap2 {
				t.Fatalf("payload[1] (rmgr) = %d, want %d (RmgrHeap2)", payload[1], canonicalRmgrHeap2)
			}
			if payload[2] != tc.wantInfo {
				t.Fatalf("payload[2] (info) = 0x%02x, want 0x%02x", payload[2], tc.wantInfo)
			}
			gotXID := binary.LittleEndian.Uint32(payload[3:7])
			if gotXID != xid {
				t.Fatalf("payload[3:7] (xid) = %d, want %d", gotXID, xid)
			}

			// Total: 7 (envelope) + 25 (block ref) + 2 (main-data hdr) + 8192 (FPI) + 2 (main data content) = 8228.
			const wantLen = canonicalHeaderSize + 25 + 2 + storage.BlockSize + 2
			if len(payload) != wantLen {
				t.Fatalf("len(payload) = %d, want %d", len(payload), wantLen)
			}

			body := payload[canonicalHeaderSize:]
			// Block reference header: block data_len must be 0 (no sub-records).
			dataLen := binary.LittleEndian.Uint16(body[2:4])
			if dataLen != 0 {
				t.Errorf("data_len = %d, want 0 (FPI-only, no block-data sub-records)", dataLen)
			}
			bn := binary.LittleEndian.Uint32(body[21:25])
			if bn != uint32(blk) {
				t.Errorf("blockNum = %d, want %d", bn, blk)
			}

			// FPI bytes.
			fpi := body[27 : 27+storage.BlockSize]
			for i, b := range fpi {
				if b != page[i] {
					t.Fatalf("FPI mismatch at byte %d: got 0x%02x, want 0x%02x", i, b, page[i])
				}
			}

			// Main data: xl_heap_prune{reason, flags=0}.
			mdContent := body[27+storage.BlockSize:]
			if len(mdContent) != 2 {
				t.Fatalf("main data content len = %d, want 2", len(mdContent))
			}
			if mdContent[0] != tc.wantReason {
				t.Errorf("main data reason = %d, want %d", mdContent[0], tc.wantReason)
			}
			if mdContent[1] != 0 {
				t.Errorf("main data flags = 0x%02x, want 0", mdContent[1])
			}
		})
	}
}

// TestPgCanonicalHeapPrune_NilLogFn verifies the nil-hook no-op guard shared
// by every PgCanonicalHeap* constructor.
func TestPgCanonicalHeapPrune_NilLogFn(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 5, RelOid: 16400}
	page := make(storage.Page, storage.BlockSize)
	if _, err := PgCanonicalHeapPrune(rel, 0, page, 42, true, nil); err != nil {
		t.Fatalf("unexpected error with nil logFn: %v", err)
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

	// In the correct PG XLog body format, the main data header (tag+len) comes
	// at body[25:27], the FPI at body[27:27+BlockSize], and content after that.
	body := payload[canonicalHeaderSize:]
	if body[25] != canonicalXlogDataShort {
		t.Errorf("main data tag = 0x%02x, want xlrBlockIDDataShort", body[25])
	}
	if body[26] != 2 {
		t.Errorf("main data len = %d, want 2", body[26])
	}
	mdContent := body[27+storage.BlockSize:]
	gotOffnum := binary.LittleEndian.Uint16(mdContent[0:2])
	if gotOffnum != offnum {
		t.Errorf("offnum = %d, want %d", gotOffnum, offnum)
	}
}

// TestPgCanonicalHeapInsert_NilLogFn verifies that a nil logFn is a no-op.
func TestPgCanonicalHeapInsert_NilLogFn(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 5, RelOid: 1259}
	page := make(storage.Page, storage.BlockSize)
	if _, err := PgCanonicalHeapInsert(rel, 0, page, 1, 42, nil); err != nil {
		t.Fatalf("unexpected error with nil logFn: %v", err)
	}
}

// TestRelationMapUpdateMap_Stub verifies that the stub returns nil.
func TestRelationMapUpdateMap_Stub(t *testing.T) {
	if err := RelationMapUpdateMap(5, 1259, 1259, false, nil); err != nil {
		t.Fatalf("RelationMapUpdateMap stub returned error: %v", err)
	}
}


// TestBuildCanonicalXactCommitPayload pins the byte layout of the
// canonical XLOG_XACT_COMMIT WAL record (M0106-0010 batched-46). The
// PG18 standby's `xact_redo_commit` walks the wire bytes directly, so
// any drift here will silently break replication of post-basebackup
// commits.
func TestBuildCanonicalXactCommitPayload(t *testing.T) {
	const xid = uint32(4711)
	const xactTime int64 = 1234567890

	payload := BuildCanonicalXactCommitPayload(xid, xactTime)

	// Envelope: kind(1) + rmgr(1) + info(1) + xid(4) = 7 bytes.
	// Body: main-data-tag(1) + len(1) + xact_time(8) = 10 bytes.
	const wantLen = canonicalHeaderSize + 2 + 8
	if len(payload) != wantLen {
		t.Fatalf("len(payload) = %d, want %d", len(payload), wantLen)
	}

	if payload[0] != RecordKindCanonical {
		t.Errorf("payload[0] = 0x%02x, want RecordKindCanonical (0x%02x)", payload[0], RecordKindCanonical)
	}
	if payload[1] != canonicalRmgrXact {
		t.Errorf("rmgr = %d, want RM_XACT_ID (%d)", payload[1], canonicalRmgrXact)
	}
	if payload[2] != canonicalInfoXactCommit {
		t.Errorf("info = 0x%02x, want XLOG_XACT_COMMIT (0x%02x)", payload[2], canonicalInfoXactCommit)
	}
	if gotXID := binary.LittleEndian.Uint32(payload[3:7]); gotXID != xid {
		t.Errorf("xid = %d, want %d", gotXID, xid)
	}

	// Body inspection.
	body := payload[canonicalHeaderSize:]
	if body[0] != canonicalXlogDataShort {
		t.Errorf("main-data tag = 0x%02x, want xlrBlockIDDataShort (0x%02x)", body[0], canonicalXlogDataShort)
	}
	if body[1] != 8 {
		t.Errorf("main-data len = %d, want 8 (sizeof xl_xact_commit)", body[1])
	}
	gotXactTime := int64(binary.LittleEndian.Uint64(body[2:10]))
	if gotXactTime != xactTime {
		t.Errorf("xact_time = %d, want %d", gotXactTime, xactTime)
	}
}

// TestBuildCanonicalXactAbortPayload mirrors the commit-record test
// for the abort variant. Only the info byte differs (0x20 vs 0x00).
func TestBuildCanonicalXactAbortPayload(t *testing.T) {
	const xid = uint32(99)
	const xactTime int64 = -42 // negative is legal: pre-Y2K test value

	payload := BuildCanonicalXactAbortPayload(xid, xactTime)

	if payload[2] != canonicalInfoXactAbort {
		t.Errorf("info = 0x%02x, want XLOG_XACT_ABORT (0x%02x)", payload[2], canonicalInfoXactAbort)
	}
	if payload[1] != canonicalRmgrXact {
		t.Errorf("rmgr = %d, want RM_XACT_ID (%d)", payload[1], canonicalRmgrXact)
	}
	if gotXID := binary.LittleEndian.Uint32(payload[3:7]); gotXID != xid {
		t.Errorf("xid = %d, want %d", gotXID, xid)
	}
	body := payload[canonicalHeaderSize:]
	gotXactTime := int64(binary.LittleEndian.Uint64(body[2:10]))
	if gotXactTime != xactTime {
		t.Errorf("xact_time = %d, want %d", gotXactTime, xactTime)
	}
}

// TestPgCanonicalXactCommit_NilLogFnIsNoop guards the legacy
// (non-PageHeaders) WAL path: when the caller passes a nil
// LogCanonicalFunc, the encoder must short-circuit instead of
// panicking on a nil callback.
func TestPgCanonicalXactCommit_NilLogFnIsNoop(t *testing.T) {
	endLSN, err := PgCanonicalXactCommit(1, 0, nil)
	if err != nil {
		t.Fatalf("nil logFn: unexpected error %v", err)
	}
	if endLSN != 0 {
		t.Fatalf("nil logFn: endLSN = %d, want 0", endLSN)
	}
	endLSN, err = PgCanonicalXactAbort(1, 0, nil)
	if err != nil {
		t.Fatalf("nil logFn (abort): unexpected error %v", err)
	}
	if endLSN != 0 {
		t.Fatalf("nil logFn (abort): endLSN = %d, want 0", endLSN)
	}
}

// TestPgCanonicalXactCommit_RouteThroughLogFn proves the encoder
// hands the payload bytes to the caller-supplied LogCanonicalFunc
// without modification and returns whatever endLSN the callback
// reports — the contract the initdb wal-marker logger relies on.
func TestPgCanonicalXactCommit_RouteThroughLogFn(t *testing.T) {
	const wantEndLSN = uint64(0xCAFEBABEDEADBEEF)
	var captured []byte
	logFn := func(payload []byte) (uint64, error) {
		captured = append(captured, payload...)
		return wantEndLSN, nil
	}
	got, err := PgCanonicalXactCommit(7, 123, logFn)
	if err != nil {
		t.Fatalf("PgCanonicalXactCommit error: %v", err)
	}
	if got != wantEndLSN {
		t.Fatalf("endLSN = %#x, want %#x", got, wantEndLSN)
	}
	want := BuildCanonicalXactCommitPayload(7, 123)
	if len(captured) != len(want) {
		t.Fatalf("captured len = %d, want %d", len(captured), len(want))
	}
	for i := range want {
		if captured[i] != want[i] {
			t.Fatalf("byte %d: got 0x%02x, want 0x%02x", i, captured[i], want[i])
		}
	}
}
