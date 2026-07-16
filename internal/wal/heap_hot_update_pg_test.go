package wal

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestApplyRecordReplaysPGHeapHotUpdate inserts a tuple, then HOT-updates it via
// a PG xl_heap_update (HOT) record, and asserts replay: the old tuple gets
// xmax + t_ctid->new + HEAP_HOT_UPDATED, and the new tuple lands at new_offnum
// with HEAP_ONLY_TUPLE + its own xmin.
func TestApplyRecordReplaysPGHeapHotUpdate(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 907, Fork: storage.MainFork}

	// Old version at block 0 / slot 1.
	oldTup := storage.NewHeapTuple(42, storage.InvalidTransactionID, []byte("old"))
	oldTup.Header.CTID = storage.ItemPointer{Block: 0, Offset: 1}
	oldBytes, err := oldTup.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	insertFramed, err := EncodeHeapInsertPG(rel, 0, 1, oldBytes, false)
	if err != nil {
		t.Fatal(err)
	}
	applyPGRecord(t, mgr, insertFramed, 100)

	// HOT update: new version at slot 2, updater xid 99.
	const xmax = storage.TransactionID(99)
	newTup := storage.NewHeapTuple(xmax, storage.InvalidTransactionID, []byte("new"))
	newTup.Header.Infomask |= storage.HeapOnlyTuple // HOT new version
	newBytes, err := newTup.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	framed, err := EncodeHeapHotUpdatePG(rel, 0, 1, 2, xmax, newBytes)
	if err != nil {
		t.Fatal(err)
	}
	applyPGRecord(t, mgr, framed, 200)

	page := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, page); err != nil {
		t.Fatal(err)
	}

	oldAfter, err := storage.PageGetHeapTuple(page, 1)
	if err != nil {
		t.Fatal(err)
	}
	if oldAfter.Header.Xmax != xmax {
		t.Fatalf("old t_xmax = %d, want %d", oldAfter.Header.Xmax, xmax)
	}
	if oldAfter.Header.CTID != (storage.ItemPointer{Block: 0, Offset: 2}) {
		t.Fatalf("old t_ctid = %+v, want {0,2} (->new)", oldAfter.Header.CTID)
	}
	if oldAfter.Header.Infomask&storage.HeapHotUpdated == 0 {
		t.Fatalf("old tuple missing HEAP_HOT_UPDATED")
	}

	newAfter, err := storage.PageGetHeapTuple(page, 2)
	if err != nil {
		t.Fatal(err)
	}
	if newAfter.Header.Xmin != xmax {
		t.Fatalf("new t_xmin = %d, want %d", newAfter.Header.Xmin, xmax)
	}
	if newAfter.Header.CTID != (storage.ItemPointer{Block: 0, Offset: 2}) {
		t.Fatalf("new t_ctid = %+v, want self {0,2}", newAfter.Header.CTID)
	}
	if newAfter.Header.Infomask&storage.HeapOnlyTuple == 0 {
		t.Fatalf("new tuple missing HEAP_ONLY_TUPLE")
	}
	if string(newAfter.Data) != "new" {
		t.Fatalf("new tuple data = %q, want %q", newAfter.Data, "new")
	}
}
