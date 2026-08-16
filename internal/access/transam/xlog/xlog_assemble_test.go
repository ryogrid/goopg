package xlog

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// locator12 builds the 12-byte on-wire RelFileLocator {spcOid, dbOid, relOid}.
func locator12(spc, db, rel uint32) []byte {
	out := make([]byte, 12)
	binary.LittleEndian.PutUint32(out[0:4], spc)
	binary.LittleEndian.PutUint32(out[4:8], db)
	binary.LittleEndian.PutUint32(out[8:12], rel)
	return out
}

// TestAssembleXLogRecord_SharedCatalogLocator asserts the B4.1a invariant: a
// shared catalog (DBOid==0, files under global/) encodes its RelFileLocator
// with spcOid=GLOBALTABLESPACE_OID(1664)/dbOid=0 so a real PostgreSQL standby
// routes the replayed block to its own global/, while a per-DB relation keeps
// spcOid=DEFAULTTABLESPACE_OID(1663). Both must round-trip back to goopg's
// TblOid=0 convention (DBOid then selects global/ vs base/<db>).
func TestAssembleXLogRecord_SharedCatalogLocator(t *testing.T) {
	shared := storage.RelFileNode{DBOid: 0, RelOid: 1213, Fork: storage.MainFork} // pg_tablespace
	perDB := storage.RelFileNode{DBOid: 5, RelOid: 1259, Fork: storage.MainFork}  // pg_class in postgres DB

	sharedBody := mustAssemble(t, nil, []BlockRef{{ID: 0, Rel: shared, Block: 0, Data: []byte("row")}})
	perDBBody := mustAssemble(t, nil, []BlockRef{{ID: 0, Rel: perDB, Block: 0, Data: []byte("row")}})

	if !bytes.Contains(sharedBody, locator12(pgGlobalTableSpaceOID, 0, 1213)) {
		t.Fatalf("shared-catalog locator missing: want spcOid=1664/dbOid=0/relOid=1213")
	}
	if bytes.Contains(sharedBody, locator12(pgDefaultTableSpaceOID, 0, 1213)) {
		t.Fatalf("shared catalog wrongly encoded as pg_default (1663)")
	}
	if !bytes.Contains(perDBBody, locator12(pgDefaultTableSpaceOID, 5, 1259)) {
		t.Fatalf("per-DB locator missing: want spcOid=1663/dbOid=5/relOid=1259")
	}

	if got := decodeAssembled(t, sharedBody).Blocks[0].Rel; got != shared {
		t.Fatalf("shared rel round-trip: got %+v want %+v", got, shared)
	}
	if got := decodeAssembled(t, perDBBody).Blocks[0].Rel; got != perDB {
		t.Fatalf("per-DB rel round-trip: got %+v want %+v", got, perDB)
	}
}

// decodeAssembled runs an assembled record body back through the faithful
// decoder (parseXLogRecordData) — the round-trip contract for the encoder.
func decodeAssembled(t *testing.T, wrapped []byte) *XLogDecodedRecord {
	t.Helper()
	// The header rmid/info/xid are irrelevant to block/main-data decoding
	// (they only matter for the native-payload fast path, which is skipped
	// whenever there are block refs); use a neutral header.
	hdr := XLogRecord{Rmid: RmgrXLog}
	decoded, err := parseXLogRecordData(hdr, wrapped)
	if err != nil {
		t.Fatalf("parseXLogRecordData: %v", err)
	}
	if decoded.XLog == nil {
		t.Fatalf("decoded.XLog is nil")
	}
	return decoded.XLog
}

func mustAssemble(t *testing.T, mainData []byte, blocks []BlockRef) []byte {
	t.Helper()
	body, err := assembleXLogRecord(mainData, blocks)
	if err != nil {
		t.Fatalf("assembleXLogRecord: %v", err)
	}
	return body
}

func TestAssembleXLogRecord_MainDataOnly(t *testing.T) {
	for _, mainData := range [][]byte{
		[]byte("short main data"),
		bytes.Repeat([]byte{0xAB}, 300), // forces the LONG main-data header
	} {
		body := mustAssemble(t, mainData, nil)
		dec := decodeAssembled(t, body)
		if len(dec.Blocks) != 0 {
			t.Fatalf("want 0 blocks, got %d", len(dec.Blocks))
		}
		if !bytes.Equal(dec.MainData, mainData) {
			t.Fatalf("main data round-trip mismatch: got %d bytes", len(dec.MainData))
		}
	}
}

func TestAssembleXLogRecord_OneBlockWithData(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 16400, RelOid: 24576, Fork: storage.MainFork}
	blkData := []byte("tuple-bytes")
	body := mustAssemble(t, []byte("hdr"), []BlockRef{{
		ID:    0,
		Rel:   rel,
		Block: 7,
		Data:  blkData,
	}})
	dec := decodeAssembled(t, body)
	if len(dec.Blocks) != 1 {
		t.Fatalf("want 1 block, got %d", len(dec.Blocks))
	}
	b := dec.Blocks[0]
	if b.Rel != rel {
		t.Fatalf("rel mismatch: got %+v want %+v", b.Rel, rel)
	}
	if b.Block != 7 {
		t.Fatalf("block number mismatch: got %d", b.Block)
	}
	if b.HasImage {
		t.Fatalf("unexpected image on data-only block")
	}
	if !bytes.Equal(b.Data, blkData) {
		t.Fatalf("block data mismatch: got %q", b.Data)
	}
	if !bytes.Equal(dec.MainData, []byte("hdr")) {
		t.Fatalf("main data mismatch: got %q", dec.MainData)
	}
}

func TestAssembleXLogRecord_OneBlockWithImageHole(t *testing.T) {
	const lower, upper = 100, 8000 // hole = [100:8000], all zeros
	page := make(storage.Page, storage.BlockSize)
	// Non-zero content only OUTSIDE the hole so the reconstructed page
	// (which zeroes the hole) matches byte-for-byte.
	for i := storage.SizeOfPageHeaderData; i < lower; i++ {
		page[i] = byte(i)
	}
	for i := upper; i < storage.BlockSize; i++ {
		page[i] = byte(i * 7)
	}
	h := storage.MustHeader(page)
	h.SetLower(lower)
	h.SetUpper(upper)

	rel := storage.RelFileNode{DBOid: 16400, RelOid: 30000, Fork: storage.MainFork}
	body := mustAssemble(t, nil, []BlockRef{{
		ID:    0,
		Rel:   rel,
		Block: 3,
		Image: &FullPageImage{Page: page, Apply: true},
	}})
	dec := decodeAssembled(t, body)
	if len(dec.Blocks) != 1 {
		t.Fatalf("want 1 block, got %d", len(dec.Blocks))
	}
	b := dec.Blocks[0]
	if !b.HasImage {
		t.Fatalf("expected HasImage")
	}
	if !b.ImageApply {
		t.Fatalf("expected ImageApply (BKPIMAGE_APPLY)")
	}
	if b.Rel != rel || b.Block != 3 {
		t.Fatalf("rel/block mismatch: got %+v blk=%d", b.Rel, b.Block)
	}
	if !bytes.Equal([]byte(b.Image), []byte(page)) {
		t.Fatalf("full-page image did not round-trip byte-for-byte")
	}
}

func TestAssembleXLogRecord_TwoBlocksSameRel(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 16400, RelOid: 40000, Fork: storage.MainFork}
	body := mustAssemble(t, nil, []BlockRef{
		{ID: 0, Rel: rel, Block: 5, Data: []byte("x")},
		{ID: 1, Rel: rel, Block: 6, Data: []byte("yy"), SameRel: true},
	})
	dec := decodeAssembled(t, body)
	if len(dec.Blocks) != 2 {
		t.Fatalf("want 2 blocks, got %d", len(dec.Blocks))
	}
	if dec.Blocks[0].Rel != rel || dec.Blocks[1].Rel != rel {
		t.Fatalf("SAME_REL back-reference failed: %+v / %+v", dec.Blocks[0].Rel, dec.Blocks[1].Rel)
	}
	if !bytes.Equal(dec.Blocks[0].Data, []byte("x")) || !bytes.Equal(dec.Blocks[1].Data, []byte("yy")) {
		t.Fatalf("block data mismatch: %q / %q", dec.Blocks[0].Data, dec.Blocks[1].Data)
	}
}

func TestAssembleXLogRecord_ImageDataAndMain(t *testing.T) {
	// One block carrying BOTH an image and block data, plus main data —
	// exercises the full payload ordering (image, then data, then main).
	page := make(storage.Page, storage.BlockSize)
	for i := storage.SizeOfPageHeaderData; i < 64; i++ {
		page[i] = byte(i)
	}
	h := storage.MustHeader(page)
	h.SetLower(64)
	h.SetUpper(8192) // no hole (upper == BlockSize, hole len 0-length region)

	rel := storage.RelFileNode{DBOid: 1, RelOid: 1259, Fork: storage.MainFork}
	blkData := []byte("blkdata")
	mainData := []byte("maindata")
	body := mustAssemble(t, mainData, []BlockRef{{
		ID: 0, Rel: rel, Block: 0, Data: blkData, Image: &FullPageImage{Page: page},
	}})
	dec := decodeAssembled(t, body)
	if len(dec.Blocks) != 1 {
		t.Fatalf("want 1 block, got %d", len(dec.Blocks))
	}
	b := dec.Blocks[0]
	if !b.HasImage || !bytes.Equal([]byte(b.Image), []byte(page)) {
		t.Fatalf("image round-trip failed")
	}
	if !bytes.Equal(b.Data, blkData) {
		t.Fatalf("block data mismatch: %q", b.Data)
	}
	if !bytes.Equal(dec.MainData, mainData) {
		t.Fatalf("main data mismatch: %q", dec.MainData)
	}
}

func TestAssembleXLogRecord_Errors(t *testing.T) {
	if _, err := assembleXLogRecord(nil, []BlockRef{{ID: xlrMaxBlockID + 1}}); err == nil {
		t.Fatalf("expected error for out-of-range block id")
	}
	if _, err := assembleXLogRecord(nil, []BlockRef{{ID: 0, SameRel: true}}); err == nil {
		t.Fatalf("expected error for SAME_REL on first block")
	}
	if _, err := assembleXLogRecord(nil, []BlockRef{{ID: 0, Data: bytes.Repeat([]byte{1}, 0x10000)}}); err == nil {
		t.Fatalf("expected error for oversized block data")
	}
	bad := &FullPageImage{Page: make(storage.Page, 100)}
	if _, err := assembleXLogRecord(nil, []BlockRef{{ID: 0, Image: bad}}); err == nil {
		t.Fatalf("expected error for wrong-size page image")
	}
}
