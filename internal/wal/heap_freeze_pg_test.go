package wal

import (
	"reflect"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestEncodeHeapFreezePGRoundTripAndReplay checks the freeze half of the
// composite xl_heap_prune: the record decodes back to the same frozen slots (via
// the freeze plan's trailing offset array), and replay rewrites the tuple's xmin
// to FrozenTransactionId.
func TestEncodeHeapFreezePGRoundTripAndReplay(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 908, Fork: storage.MainFork}

	// Decode round-trip.
	frozen := []uint16{1, 3, 5}
	framed, err := EncodeHeapFreezePG(rel, 2, frozen)
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
	if dec.Header.Rmid != RmgrHeap2 || dec.Header.Info != xlogHeap2PruneVacuumClean {
		t.Fatalf("rmid/info = %d/%#x, want RmgrHeap2/VACUUM_CLEANUP", dec.Header.Rmid, dec.Header.Info)
	}
	block, ok := xlogBlockRefByID(dec.XLog, 0)
	if !ok {
		t.Fatalf("decoded record missing block 0")
	}
	gotR, gotU, gotF, err := decodeXLogHeapPrune(dec.XLog.MainData, block.Data)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotR) != 0 || len(gotU) != 0 {
		t.Fatalf("unexpected redirect/unused: %v / %v", gotR, gotU)
	}
	if !reflect.DeepEqual(gotF, frozen) {
		t.Fatalf("frozen slots = %v, want %v", gotF, frozen)
	}

	// Real replay: insert a tuple, then freeze it.
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	tup := storage.NewHeapTuple(42, storage.InvalidTransactionID, []byte("v"))
	tup.Header.CTID = storage.ItemPointer{Block: 0, Offset: 1}
	tupBytes, err := tup.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	insFramed, err := EncodeHeapInsertPG(rel, 0, 1, tupBytes, false)
	if err != nil {
		t.Fatal(err)
	}
	applyPGRecord(t, mgr, insFramed, 100)

	frzFramed, err := EncodeHeapFreezePG(rel, 0, []uint16{1})
	if err != nil {
		t.Fatal(err)
	}
	applyPGRecord(t, mgr, frzFramed, 200)

	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, page); err != nil {
		t.Fatal(err)
	}
	frozenTup, err := storage.PageGetHeapTuple(page, 1)
	if err != nil {
		t.Fatal(err)
	}
	if frozenTup.Header.Xmin != storage.FrozenTransactionID {
		t.Fatalf("frozen t_xmin = %d, want FrozenTransactionID (%d)", frozenTup.Header.Xmin, storage.FrozenTransactionID)
	}
}
