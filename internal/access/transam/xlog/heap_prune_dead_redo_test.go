package xlog

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestReplayPGHeapPruneDeadItemsLikeRuntime pins the sibling agreement for the
// XLHP_HAS_DEAD_ITEMS sub-record (M0145-0008v): a prune that leaves a dead
// non-HOT tuple LP_DEAD and marks a dead HEAP_ONLY tuple LP_UNUSED must replay
// to the byte-identical page (modulo pd_lsn).
func TestReplayPGHeapPruneDeadItemsLikeRuntime(t *testing.T) {
	saved := storage.XidCommitted
	storage.XidCommitted = func(storage.TransactionID) bool { return true }
	defer func() { storage.XidCommitted = saved }()

	const (
		deadXID    = storage.TransactionID(50)
		oldestXmin = storage.TransactionID(100)
	)
	base := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(base); err != nil {
		t.Fatal(err)
	}
	// Slot 1: a deleted non-HOT tuple (index entry points here) -> LP_DEAD.
	if _, err := storage.PageAddHeapTuple(base, storage.NewHeapTuple(10, deadXID, []byte("deleted-row-payload"))); err != nil {
		t.Fatal(err)
	}
	// Slot 2: a dead HEAP_ONLY tuple (no index entry) -> LP_UNUSED.
	ho := storage.NewHeapTuple(10, deadXID, []byte("heap-only-version"))
	ho.Header.SetHeapOnly()
	if _, err := storage.PageAddHeapTuple(base, ho); err != nil {
		t.Fatal(err)
	}
	// Slot 3: live.
	if _, err := storage.PageAddHeapTuple(base, storage.NewHeapTuple(10, storage.InvalidTransactionID, []byte("live"))); err != nil {
		t.Fatal(err)
	}
	storage.MustHeader(base).SetPruneXID(uint32(deadXID))

	runtimePage := make(storage.Page, storage.BlockSize)
	copy(runtimePage, base)
	result, err := storage.PagePruneOpt(runtimePage, oldestXmin)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Dead) != 1 || result.Dead[0] != 1 || len(result.Unused) != 1 || result.Unused[0] != 2 {
		t.Fatalf("fixture must prune to Dead=[1] Unused=[2], got %+v", result)
	}

	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()
	rel := storage.RelFileNode{DBOid: 1, RelOid: 3034, Fork: storage.MainFork}
	if _, err := mgr.Extend(rel, base); err != nil {
		t.Fatal(err)
	}
	framed, err := EncodeHeapPruneOptPG(rel, 0, result.Redirects, result.Dead, result.Unused)
	if err != nil {
		t.Fatal(err)
	}
	const recEnd = 4096
	applyPGRecord(t, mgr, framed, recEnd)
	replayed := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, replayed); err != nil {
		t.Fatal(err)
	}
	if dead, err := storage.PageDeadItems(replayed); err != nil || len(dead) != 1 || dead[0] != 1 {
		t.Fatalf("replayed LP_DEAD items = %v (err %v), want [1]", dead, err)
	}
	storage.MustHeader(runtimePage).SetLSN(storage.LSN(recEnd))
	for i := range runtimePage {
		if runtimePage[i] != replayed[i] {
			t.Fatalf("replayed page diverges from the runtime-pruned page at byte %d (got %#x, want %#x)",
				i, replayed[i], runtimePage[i])
		}
	}
}
