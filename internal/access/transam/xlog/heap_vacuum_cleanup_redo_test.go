package xlog

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestReplayPGHeapVacuumCleanupLikeRuntime pins the sibling agreement for
// VACUUM's second heap pass (M0145-0008v): the XLOG_HEAP2_PRUNE_VACUUM_CLEANUP
// record replays to the page storage.PageVacuumDeadItems produced at runtime,
// byte for byte (modulo pd_lsn), including the truncated line-pointer array.
func TestReplayPGHeapVacuumCleanupLikeRuntime(t *testing.T) {
	base := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(base); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if _, err := storage.PageAddHeapTuple(base, storage.NewHeapTuple(1, storage.InvalidTransactionID, []byte("payload"))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := storage.PruneHeapPageBySlots(base, nil, []uint16{2, 4}); err != nil {
		t.Fatal(err)
	}
	runtimePage := make(storage.Page, storage.BlockSize)
	copy(runtimePage, base)
	if _, err := storage.PageVacuumDeadItems(runtimePage, []uint16{2, 4}); err != nil {
		t.Fatal(err)
	}

	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()
	rel := storage.RelFileNode{DBOid: 1, RelOid: 3035, Fork: storage.MainFork}
	if _, err := mgr.Extend(rel, base); err != nil {
		t.Fatal(err)
	}
	framed, err := EncodeHeapVacuumCleanupPG(rel, 0, []uint16{2, 4})
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
	if dec.Header.Info != xlogHeap2PruneVacuumClean || dec.XLog.MainData[1]&xlhpCleanupLock != 0 {
		t.Fatalf("info=%#x flags=%#x: want PRUNE_VACUUM_CLEANUP without XLHP_CLEANUP_LOCK", dec.Header.Info, dec.XLog.MainData[1])
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
