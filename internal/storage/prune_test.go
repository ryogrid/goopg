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
	root.Header.Infomask |= HeapHotUpdated
	root.Header.CTID = ItemPointer{Block: 0, Offset: 2}
	s1, err := PageAddHeapTuple(page, root)
	if err != nil || s1 != 1 {
		t.Fatalf("add root: slot=%d err=%v", s1, err)
	}

	// Slot 2: HOT-only successor, still live (xmin=5, xmax=0)
	succ := NewHeapTuple(xid, InvalidTransactionID, []byte("succ"))
	succ.Header.Infomask |= HeapOnlyTuple
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
