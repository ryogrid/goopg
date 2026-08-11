package storage

import (
	"testing"
)

// TestPagePruneOptBasic verifies that PagePruneOpt reclaims a dead tuple
// (xmax < oldestXmin, not lock-only) and clears pd_prune_xid.
func TestPagePruneOptBasic(t *testing.T) {
	page := make(Page, BlockSize)
	if err := InitPage(page); err != nil {
		t.Fatal(err)
	}

	xmin := TransactionID(1)
	xmax := TransactionID(5)
	oldestXmin := TransactionID(10) // xmax(5) < oldestXmin(10) → dead

	dead := NewHeapTuple(xmin, xmax, []byte("dead"))
	slot1, err := PageAddHeapTuple(page, dead)
	if err != nil || slot1 != 1 {
		t.Fatalf("add dead tuple: slot=%d err=%v", slot1, err)
	}
	live := NewHeapTuple(xmin, InvalidTransactionID, []byte("live"))
	slot2, err := PageAddHeapTuple(page, live)
	if err != nil || slot2 != 2 {
		t.Fatalf("add live tuple: slot=%d err=%v", slot2, err)
	}

	MustHeader(page).SetPruneXID(uint32(xmax))

	result, err := PagePruneOpt(page, oldestXmin)
	if err != nil {
		t.Fatalf("PagePruneOpt: %v", err)
	}
	// Dead tuple is a standalone delete (no HOT flags) → marked unused.
	if len(result.Unused) != 1 || result.Unused[0] != 1 {
		t.Errorf("expected Unused=[1], got %v", result.Unused)
	}
	if len(result.Redirects) != 0 {
		t.Errorf("expected no redirects, got %v", result.Redirects)
	}

	if got := MustHeader(page).PruneXID(); got != 0 {
		t.Errorf("pd_prune_xid should be 0 after prune, got %d", got)
	}

	item, err := readItemID(page, 0)
	if err != nil {
		t.Fatal(err)
	}
	if item.Flags != ItemIDUnused {
		t.Errorf("slot 1 should be ItemIDUnused after prune, got flags=%d", item.Flags)
	}

	got, err := PageGetHeapTuple(page, 2)
	if err != nil {
		t.Fatalf("live tuple slot 2 after prune: %v", err)
	}
	if string(got.Data) != "live" {
		t.Errorf("live tuple data mismatch: %q", got.Data)
	}
}

// TestPagePruneOptFastPathSkips verifies the fast path: when pd_prune_xid
// is 0, PagePruneOpt returns immediately without touching the page.
func TestPagePruneOptFastPathSkips(t *testing.T) {
	page := make(Page, BlockSize)
	if err := InitPage(page); err != nil {
		t.Fatal(err)
	}
	dead := NewHeapTuple(1, 5, []byte("x"))
	if _, err := PageAddHeapTuple(page, dead); err != nil {
		t.Fatal(err)
	}
	// pd_prune_xid = 0 → skip.
	result, err := PagePruneOpt(page, 10)
	if err != nil {
		t.Fatalf("PagePruneOpt: %v", err)
	}
	if len(result.Unused)+len(result.Redirects) != 0 {
		t.Errorf("expected no pruning (pd_prune_xid=0), got %+v", result)
	}
}

// TestPagePruneOptSkipsLiveTuples verifies that tuples with xmax >= oldestXmin
// are not pruned.
func TestPagePruneOptSkipsLiveTuples(t *testing.T) {
	page := make(Page, BlockSize)
	if err := InitPage(page); err != nil {
		t.Fatal(err)
	}

	xmax := TransactionID(15)
	oldestXmin := TransactionID(10) // 15 >= 10 → still visible

	tup := NewHeapTuple(1, xmax, []byte("maybe"))
	if _, err := PageAddHeapTuple(page, tup); err != nil {
		t.Fatal(err)
	}
	MustHeader(page).SetPruneXID(uint32(xmax))

	result, err := PagePruneOpt(page, oldestXmin)
	if err != nil {
		t.Fatalf("PagePruneOpt: %v", err)
	}
	if len(result.Unused)+len(result.Redirects) != 0 {
		t.Errorf("tuple with xmax >= oldestXmin should not be pruned, got %+v", result)
	}
}

// TestPageSetHeapTupleXmaxUpdatesPruneXID verifies that PageSetHeapTupleXmax
// advances pd_prune_xid.
func TestPageSetHeapTupleXmaxUpdatesPruneXID(t *testing.T) {
	page := make(Page, BlockSize)
	if err := InitPage(page); err != nil {
		t.Fatal(err)
	}
	tup := NewHeapTuple(1, 0, []byte("r"))
	if _, err := PageAddHeapTuple(page, tup); err != nil {
		t.Fatal(err)
	}

	if err := PageSetHeapTupleXmax(page, 1, 7); err != nil {
		t.Fatal(err)
	}
	if got := TransactionID(MustHeader(page).PruneXID()); got != 7 {
		t.Errorf("pd_prune_xid should be 7, got %d", got)
	}

	tup2 := NewHeapTuple(1, 0, []byte("s"))
	slot2, err := PageAddHeapTuple(page, tup2)
	if err != nil {
		t.Fatal(err)
	}
	if err := PageSetHeapTupleXmax(page, slot2, 3); err != nil {
		t.Fatal(err)
	}
	// Smaller xmax must not decrease pd_prune_xid.
	if got := TransactionID(MustHeader(page).PruneXID()); got != 7 {
		t.Errorf("pd_prune_xid should remain 7 (not decrease to 3), got %d", got)
	}
}

// TestPagePruneOptSkipsLockOnly verifies that row-locked tuples are not pruned.
func TestPagePruneOptSkipsLockOnly(t *testing.T) {
	page := make(Page, BlockSize)
	if err := InitPage(page); err != nil {
		t.Fatal(err)
	}
	tup := NewHeapTuple(1, InvalidTransactionID, []byte("locked"))
	slot, err := PageAddHeapTuple(page, tup)
	if err != nil {
		t.Fatal(err)
	}
	if err := PageSetHeapTupleLockOnly(page, slot, 5, HeapXmaxExclLock); err != nil {
		t.Fatal(err)
	}
	MustHeader(page).SetPruneXID(5)

	result, err := PagePruneOpt(page, 10)
	if err != nil {
		t.Fatalf("PagePruneOpt: %v", err)
	}
	if len(result.Unused)+len(result.Redirects) != 0 {
		t.Errorf("lock-only tuple must not be pruned, got %+v", result)
	}
}

// TestPagePruneOptHOTChainRedirect verifies that when a dead HOT chain root
// is pruned, its line pointer becomes ItemIDRedirect pointing to the live tip.
func TestPagePruneOptHOTChainRedirect(t *testing.T) {
	page := make(Page, BlockSize)
	if err := InitPage(page); err != nil {
		t.Fatal(err)
	}

	xid := TransactionID(5) // the HOT updater
	oldestXmin := TransactionID(10)

	// Slot 1: HOT chain root — indexed, dead (xmax=5), HEAP_HOT_UPDATED, CTID→2
	root := NewHeapTuple(1, xid, []byte("root"))
	root.Header.SetHotUpdated()
	root.Header.CTID = ItemPointer{Block: 0, Offset: 2}
	s1, err := PageAddHeapTuple(page, root)
	if err != nil || s1 != 1 {
		t.Fatalf("add root: slot=%d err=%v", s1, err)
	}

	// Slot 2: HOT-only successor, still live (xmin=5, xmax=0)
	succ := NewHeapTuple(xid, InvalidTransactionID, []byte("succ"))
	succ.Header.SetHeapOnly()
	s2, err := PageAddHeapTuple(page, succ)
	if err != nil || s2 != 2 {
		t.Fatalf("add succ: slot=%d err=%v", s2, err)
	}

	MustHeader(page).SetPruneXID(uint32(xid))

	result, err := PagePruneOpt(page, oldestXmin)
	if err != nil {
		t.Fatalf("PagePruneOpt: %v", err)
	}

	// Root should be redirected to slot 2 (live tip).
	if len(result.Redirects) != 1 || result.Redirects[0] != [2]uint16{1, 2} {
		t.Errorf("expected Redirects=[(1,2)], got %v", result.Redirects)
	}
	if len(result.Unused) != 0 {
		t.Errorf("no slots should be unused (successor is live), got %v", result.Unused)
	}

	// Slot 1 must now be ItemIDRedirect → slot 2.
	item, err := readItemID(page, 0)
	if err != nil {
		t.Fatal(err)
	}
	if item.Flags != ItemIDRedirect {
		t.Errorf("slot 1 should be ItemIDRedirect, got flags=%d", item.Flags)
	}
	if item.Offset != 2 {
		t.Errorf("redirect target should be slot 2, got %d", item.Offset)
	}

	// Slot 2 should still be readable.
	got, err := PageGetHeapTuple(page, 2)
	if err != nil {
		t.Fatalf("live slot 2 after redirect: %v", err)
	}
	if string(got.Data) != "succ" {
		t.Errorf("slot 2 data: %q", got.Data)
	}
}

// TestPagePruneOptMultiXactXmax exercises the updater-bearing multixact arm of
// the prune dead-tuple test. A MultiXactId in xmax must NOT be compared to the
// oldestXmin horizon directly (a category error that could prune a live row);
// PagePruneOpt resolves the updater member via storage.ResolveMultiUpdater and
// applies the horizon test to the updater xid instead. (M0118-0003.)
func TestPagePruneOptMultiXactXmax(t *testing.T) {
	const multiID = TransactionID(1000)    // a MultiXactId, NOT an xid
	const oldestXmin = TransactionID(2000) // note: multiID < oldestXmin

	build := func(t *testing.T) Page {
		page := make(Page, BlockSize)
		if err := InitPage(page); err != nil {
			t.Fatal(err)
		}
		tup := NewHeapTuple(1, InvalidTransactionID, []byte("multi"))
		slot, err := PageAddHeapTuple(page, tup)
		if err != nil {
			t.Fatal(err)
		}
		// Updater-bearing multi: IS_MULTI set, LOCK_ONLY clear.
		if err := PageSetHeapTupleXmaxMulti(page, slot, multiID, HeapXmaxIsMulti|HeapXmaxExclLock, HeapKeysUpdated); err != nil {
			t.Fatal(err)
		}
		// pd_prune_xid must be < oldestXmin or PagePruneOpt fast-paths out before
		// it ever inspects a tuple.
		MustHeader(page).SetPruneXID(1)
		return page
	}

	setResolver := func(t *testing.T, fn func(TransactionID) (TransactionID, bool, bool)) {
		prev := ResolveMultiUpdater
		ResolveMultiUpdater = fn
		t.Cleanup(func() { ResolveMultiUpdater = prev })
	}

	pruned := func(r PruneResult) bool { return len(r.Unused)+len(r.Redirects) != 0 }

	t.Run("updater older than horizon -> dead, pruned", func(t *testing.T) {
		page := build(t)
		setResolver(t, func(x TransactionID) (TransactionID, bool, bool) {
			if x != multiID {
				t.Errorf("resolver got xmax=%d, want MultiXactId %d", x, multiID)
			}
			return 100, true, true // updater 100 < oldestXmin 2000
		})
		result, err := PagePruneOpt(page, oldestXmin)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Unused) != 1 || result.Unused[0] != 1 {
			t.Errorf("updater older than horizon must be pruned, got %+v", result)
		}
	})

	t.Run("updater newer than horizon -> live, not pruned (no category error)", func(t *testing.T) {
		page := build(t)
		setResolver(t, func(TransactionID) (TransactionID, bool, bool) {
			return 50000, true, true // updater 50000 >= oldestXmin 2000
		})
		result, err := PagePruneOpt(page, oldestXmin)
		if err != nil {
			t.Fatal(err)
		}
		if pruned(result) {
			t.Errorf("MultiXactId(%d) < oldestXmin(%d) must NOT prune when the resolved updater (50000) is newer than the horizon; got %+v (category-error regression)", multiID, oldestXmin, result)
		}
	})

	t.Run("all lockers (no updater) -> live, not pruned", func(t *testing.T) {
		page := build(t)
		setResolver(t, func(TransactionID) (TransactionID, bool, bool) {
			return InvalidTransactionID, false, true
		})
		result, err := PagePruneOpt(page, oldestXmin)
		if err != nil {
			t.Fatal(err)
		}
		if pruned(result) {
			t.Errorf("all-locker multi must not be pruned, got %+v", result)
		}
	})

	t.Run("unresolvable multi -> conservatively not pruned", func(t *testing.T) {
		page := build(t)
		setResolver(t, func(TransactionID) (TransactionID, bool, bool) {
			return InvalidTransactionID, false, false
		})
		result, err := PagePruneOpt(page, oldestXmin)
		if err != nil {
			t.Fatal(err)
		}
		if pruned(result) {
			t.Errorf("unresolvable multi must not be pruned, got %+v", result)
		}
	})

	t.Run("nil resolver -> conservatively not pruned", func(t *testing.T) {
		page := build(t)
		setResolver(t, nil)
		result, err := PagePruneOpt(page, oldestXmin)
		if err != nil {
			t.Fatal(err)
		}
		if pruned(result) {
			t.Errorf("nil resolver must not prune an IS_MULTI xmax, got %+v", result)
		}
	})
}
