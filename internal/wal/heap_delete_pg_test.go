package wal

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// applyPGRecord frames→encodes→decodes a PG-format record and applies it at the
// given EndLSN (records carry no LSN themselves; the WAL reader assigns it).
func applyPGRecord(t *testing.T, mgr *storage.Manager, framed []byte, endLSN uint64) {
	t.Helper()
	record, _, err := encodeRecordXLog(framed, 0)
	if err != nil {
		t.Fatalf("encodeRecordXLog: %v", err)
	}
	dec, err := decodeRecordXLogDetailed(record)
	if err != nil {
		t.Fatalf("decodeRecordXLogDetailed: %v", err)
	}
	if _, err := ApplyRecord(mgr, Record{EndLSN: endLSN, XLog: dec.XLog, Payload: dec.Payload}); err != nil {
		t.Fatalf("ApplyRecord: %v", err)
	}
}

// TestApplyRecordReplaysPGHeapDelete inserts a tuple via a PG xl_heap_insert
// record, then deletes it via a PG xl_heap_delete record, and asserts replay
// stamped xmax at the slot (leaving xmin/data intact).
func TestApplyRecordReplaysPGHeapDelete(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 906, Fork: storage.MainFork}

	tup := storage.NewHeapTuple(42, storage.InvalidTransactionID, []byte("row"))
	tup.Header.CTID = storage.ItemPointer{Block: 0, Offset: 1}
	tupleBytes, err := tup.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	insertFramed, err := EncodeHeapInsertPG(rel, 0, 1, tupleBytes, false)
	if err != nil {
		t.Fatal(err)
	}
	applyPGRecord(t, mgr, insertFramed, 100)

	const xmax = storage.TransactionID(99)
	delFramed, err := EncodeHeapDeletePG(rel, 0, 1, xmax, nil)
	if err != nil {
		t.Fatal(err)
	}
	applyPGRecord(t, mgr, delFramed, 200)

	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, page); err != nil {
		t.Fatal(err)
	}
	if got := storage.MustHeader(page).LSN(); got != 200 {
		t.Fatalf("pd_lsn = %d, want 200", got)
	}
	after, err := storage.PageGetHeapTuple(page, 1)
	if err != nil {
		t.Fatal(err)
	}
	if after.Header.Xmax != xmax {
		t.Fatalf("t_xmax = %d, want %d", after.Header.Xmax, xmax)
	}
	if after.Header.Xmin != 42 {
		t.Fatalf("t_xmin = %d, want 42 (delete must not touch xmin)", after.Header.Xmin)
	}
	if string(after.Data) != "row" {
		t.Fatalf("tuple data = %q, want %q", after.Data, "row")
	}
}

// TestEncodeHeapDeletePGCarriesOldTuple checks the CONTAINS_OLD_TUPLE main-data
// extension: the flag is set and the old tuple's header+data ride after the
// fixed xl_heap_delete struct (needed by the logical decoder).
func TestEncodeHeapDeletePGCarriesOldTuple(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 906, Fork: storage.MainFork}
	old := storage.NewHeapTuple(7, storage.InvalidTransactionID, []byte("oldvals"))
	old.Header.CTID = storage.ItemPointer{Block: 2, Offset: 5}
	oldBytes, err := old.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}

	framed, err := EncodeHeapDeletePG(rel, 2, 5, 99, oldBytes)
	if err != nil {
		t.Fatal(err)
	}
	record, _, err := encodeRecordXLog(framed, 0)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := decodeRecordXLogDetailed(record)
	if err != nil {
		t.Fatal(err)
	}
	xmax, offnum, _, flags, err := decodeXLogHeapDeleteMainData(dec.XLog.MainData)
	if err != nil {
		t.Fatal(err)
	}
	if xmax != 99 || offnum != 5 {
		t.Fatalf("xmax/offnum = %d/%d, want 99/5", xmax, offnum)
	}
	if flags&xlhDeleteContainsOldTuple == 0 {
		t.Fatalf("CONTAINS_OLD_TUPLE flag not set")
	}
	// Old tuple rides after the 8-byte struct: xl_heap_header(5) + data.
	if len(dec.XLog.MainData) <= sizeOfXLogHeapDeleteData {
		t.Fatalf("main data %d bytes, expected old-tuple payload past the %d-byte struct", len(dec.XLog.MainData), sizeOfXLogHeapDeleteData)
	}
}
