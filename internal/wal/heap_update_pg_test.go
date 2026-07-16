package wal

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// B0.2 (doc 02a §3): non-HOT xl_heap_update replay pins. The old tuple gets
// xmax + a forward t_ctid WITHOUT HEAP_HOT_UPDATED; the new version lands at
// new_offnum with a self-pointing ctid and no HEAP_ONLY_TUPLE.

// TestApplyRecordReplaysPGHeapUpdateSamePage: old and new versions share the
// page — PG's single-block-ref form.
func TestApplyRecordReplaysPGHeapUpdateSamePage(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 909, Fork: storage.MainFork}

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

	const xmax = storage.TransactionID(99)
	newTup := storage.NewHeapTuple(xmax, storage.InvalidTransactionID, []byte("new"))
	newBytes, err := newTup.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	framed, err := EncodeHeapUpdatePG(rel, 0, 1, 0, 2, xmax, newBytes)
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
		t.Fatalf("old t_ctid = %+v, want {0,2}", oldAfter.Header.CTID)
	}
	if oldAfter.Header.Infomask&storage.HeapHotUpdated != 0 {
		t.Fatalf("old tuple must NOT carry HEAP_HOT_UPDATED on a non-HOT update")
	}
	if oldAfter.Header.Infomask&storage.HeapXmaxInvalid != 0 {
		t.Fatalf("old tuple must have HEAP_XMAX_INVALID cleared")
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
	if newAfter.Header.Infomask&storage.HeapOnlyTuple != 0 {
		t.Fatalf("new tuple must NOT carry HEAP_ONLY_TUPLE")
	}
	if string(newAfter.Data) != "new" {
		t.Fatalf("new tuple data = %q, want %q", newAfter.Data, "new")
	}
}

// TestApplyRecordReplaysPGHeapUpdateCrossPage: the versions live on different
// pages — block 0 carries the new page + tuple, block 1 references the old
// page; replay stamps each page under its own pd_lsn idempotency.
func TestApplyRecordReplaysPGHeapUpdateCrossPage(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 910, Fork: storage.MainFork}

	// Old version on block 0; a filler insert on block 1 so the target page
	// exists (replay of heap-update does not extend relations).
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

	filler := storage.NewHeapTuple(43, storage.InvalidTransactionID, []byte("fill"))
	filler.Header.CTID = storage.ItemPointer{Block: 1, Offset: 1}
	fillerBytes, err := filler.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	fillerFramed, err := EncodeHeapInsertPG(rel, 1, 1, fillerBytes, true)
	if err != nil {
		t.Fatal(err)
	}
	applyPGRecord(t, mgr, fillerFramed, 150)

	const xmax = storage.TransactionID(77)
	newTup := storage.NewHeapTuple(xmax, storage.InvalidTransactionID, []byte("new2"))
	newBytes, err := newTup.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	framed, err := EncodeHeapUpdatePG(rel, 0, 1, 1, 2, xmax, newBytes)
	if err != nil {
		t.Fatal(err)
	}
	applyPGRecord(t, mgr, framed, 200)

	oldPage := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, oldPage); err != nil {
		t.Fatal(err)
	}
	oldAfter, err := storage.PageGetHeapTuple(oldPage, 1)
	if err != nil {
		t.Fatal(err)
	}
	if oldAfter.Header.Xmax != xmax {
		t.Fatalf("old t_xmax = %d, want %d", oldAfter.Header.Xmax, xmax)
	}
	if oldAfter.Header.CTID != (storage.ItemPointer{Block: 1, Offset: 2}) {
		t.Fatalf("old t_ctid = %+v, want {1,2} (cross-page forward link)", oldAfter.Header.CTID)
	}
	if oldAfter.Header.Infomask&storage.HeapHotUpdated != 0 {
		t.Fatalf("old tuple must NOT carry HEAP_HOT_UPDATED")
	}

	newPage := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 1, newPage); err != nil {
		t.Fatal(err)
	}
	newAfter, err := storage.PageGetHeapTuple(newPage, 2)
	if err != nil {
		t.Fatal(err)
	}
	if newAfter.Header.Xmin != xmax {
		t.Fatalf("new t_xmin = %d, want %d", newAfter.Header.Xmin, xmax)
	}
	if newAfter.Header.CTID != (storage.ItemPointer{Block: 1, Offset: 2}) {
		t.Fatalf("new t_ctid = %+v, want self {1,2}", newAfter.Header.CTID)
	}
	if string(newAfter.Data) != "new2" {
		t.Fatalf("new tuple data = %q, want %q", newAfter.Data, "new2")
	}
}
