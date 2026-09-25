package xlog

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestReplayPGHeapInsertIntoReusedLineLikeRuntime pins the sibling agreement
// for line-pointer reuse (M0145-0008v S3b): an insert the runtime placed in a
// freed interior line replays, from its PG xl_heap_insert, to the
// byte-identical page (modulo pd_lsn).
func TestReplayPGHeapInsertIntoReusedLineLikeRuntime(t *testing.T) {
	storage.SetHeapLinePointerLifecycle(true)
	defer storage.SetHeapLinePointerLifecycle(false)
	base := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(base); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if _, err := storage.PageAddHeapTuple(base, storage.NewHeapTuple(1, storage.InvalidTransactionID, []byte("payload"))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := storage.PruneHeapPageBySlots(base, nil, []uint16{2}); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.PageVacuumDeadItems(base, []uint16{2}); err != nil {
		t.Fatal(err)
	}
	tup := storage.NewHeapTuple(7, storage.InvalidTransactionID, []byte("reused-line-payload"))
	// The runtime insert path stamps a self-pointing t_ctid (the redo
	// rebuilds it from the record's offset); the freed line is slot 2.
	tup.Header.CTID = storage.ItemPointer{Block: 0, Offset: 2}
	runtimePage := make(storage.Page, storage.BlockSize)
	copy(runtimePage, base)
	slot, err := storage.PageAddHeapTuple(runtimePage, tup)
	if err != nil || slot != 2 {
		t.Fatalf("runtime insert slot %d (%v), want the freed line 2", slot, err)
	}
	raw, err := tup.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}

	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()
	rel := storage.RelFileNode{DBOid: 1, RelOid: 3036, Fork: storage.MainFork}
	if _, err := mgr.Extend(rel, base); err != nil {
		t.Fatal(err)
	}
	framed, err := EncodeHeapInsertPG(rel, 0, slot, raw, false)
	if err != nil {
		t.Fatal(err)
	}
	const recEnd = 4096
	applyPGRecord(t, mgr, framed, recEnd)
	replayed := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, replayed); err != nil {
		t.Fatal(err)
	}
	storage.MustHeader(runtimePage).SetLSN(storage.LSN(recEnd))
	for i := range runtimePage {
		if runtimePage[i] != replayed[i] {
			t.Fatalf("replayed page diverges from the runtime page at byte %d (got %#x, want %#x)", i, replayed[i], runtimePage[i])
		}
	}
}
