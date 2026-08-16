package xlog

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestReplayPGHeapPruneRedirectOnlyCompactsLikeRuntime pins the sibling
// agreement between the runtime pruner (storage.pagePruneCore, reached via
// PagePruneOpt) and the PG-format redo arm (replayDecodedXLogHeapPrune) for a
// prune that yields ONLY redirects and no now-unused slots.
//
// root-0033: the redo arm guarded its VacuumHeapPageBySlots call on
// len(unused) > 0, so a redirect-only prune left the redirected chain root's
// tuple body on the replayed page while the runtime page had reclaimed it. The
// replayed page therefore held less free space than the page the WAL described,
// and the next xl_heap_update redo died with ErrNoSpaceInPage — a crash under
// sustained write load left the cluster unstartable
// (analysis/wal-crash-restart-repro.sh).
//
// The assertion is byte equality (modulo pd_lsn, which redo stamps with the
// record's end LSN): redo must reproduce the runtime page exactly.
func TestReplayPGHeapPruneRedirectOnlyCompactsLikeRuntime(t *testing.T) {
	saved := storage.XidCommitted
	storage.XidCommitted = func(storage.TransactionID) bool { return true }
	defer func() { storage.XidCommitted = saved }()

	const (
		deadXID    = storage.TransactionID(50)
		liveXID    = storage.TransactionID(60)
		oldestXmin = storage.TransactionID(100)
	)

	// A two-tuple HOT chain: slot 1 is the (dead) chain root an index still
	// points at, slot 2 is the live tip. Pruning this shape converts slot 1 to
	// ItemIDRedirect(2) and marks NOTHING unused — the redirect-only case.
	base := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(base); err != nil {
		t.Fatal(err)
	}
	root := storage.NewHeapTuple(10, storage.InvalidTransactionID, []byte("root-version-payload"))
	if _, err := storage.PageAddHeapTuple(base, root); err != nil {
		t.Fatal(err)
	}
	tip := storage.NewHeapTuple(deadXID, storage.InvalidTransactionID, []byte("tip"))
	tip.Header.SetHeapOnly()
	tipSlot, err := storage.PageAddHeapTuple(base, tip)
	if err != nil {
		t.Fatal(err)
	}
	if tipSlot != 2 {
		t.Fatalf("tip slot = %d, want 2", tipSlot)
	}
	// Stamp the root as HOT-updated by deadXID, pointing at the tip.
	if err := storage.PageStampHotOldTuple(base, 1, deadXID, 0, tipSlot); err != nil {
		t.Fatal(err)
	}
	storage.MustHeader(base).SetPruneXID(uint32(liveXID))

	// Runtime prune on a private copy — this is the reference outcome.
	runtimePage := make(storage.Page, storage.BlockSize)
	copy(runtimePage, base)
	result, err := storage.PagePruneOpt(runtimePage, oldestXmin)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Redirects) == 0 {
		t.Fatalf("fixture did not produce a redirect: %+v", result)
	}
	if len(result.Unused) != 0 {
		t.Fatalf("fixture must be redirect-only, got unused=%v", result.Unused)
	}

	// Redo the same prune from its WAL record onto the pre-prune page.
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()
	rel := storage.RelFileNode{DBOid: 1, RelOid: 3033, Fork: storage.MainFork}
	if _, err := mgr.Extend(rel, base); err != nil {
		t.Fatal(err)
	}
	framed, err := EncodeHeapPruneOptPG(rel, 0, result.Redirects, result.Unused)
	if err != nil {
		t.Fatal(err)
	}
	const recEnd = 4096
	applyPGRecord(t, mgr, framed, recEnd)

	replayed := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 0, replayed); err != nil {
		t.Fatal(err)
	}

	// pd_lsn is expected to differ (redo stamps the record's end LSN); every
	// other byte — pd_upper and the repacked tuple region above all — must match.
	wantUpper := storage.MustHeader(runtimePage).Upper()
	gotUpper := storage.MustHeader(replayed).Upper()
	if gotUpper != wantUpper {
		t.Fatalf("replayed pd_upper = %d, runtime pd_upper = %d: redo did not reclaim the redirected root's tuple body",
			gotUpper, wantUpper)
	}
	storage.MustHeader(runtimePage).SetLSN(storage.LSN(recEnd))
	for i := range runtimePage {
		if runtimePage[i] != replayed[i] {
			t.Fatalf("replayed page diverges from the runtime-pruned page at byte %d (got %#x, want %#x)",
				i, replayed[i], runtimePage[i])
		}
	}
}
