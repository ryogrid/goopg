package storage

import "testing"

// TestPageFreezeAcrossWraparound is the review/260831-2 ST-8 guard.
// PageFreezeOldTuples compared xmin against freezeBelow with a plain unsigned
// `>=`, so once the XID counter wrapped past 2^32 every pre-wrap xmin looked
// NEWER than the (small, post-wrap) freeze horizon and nothing was frozen —
// exactly the situation freezing exists to prevent. PG compares circularly
// (TransactionIdPrecedes, heap_prepare_freeze_tuple). The same `<` bug made
// MinUnfrozenXID — the input to relfrozenxid — pick the wrong tuple.
func TestPageFreezeAcrossWraparound(t *testing.T) {
	page := make(Page, BlockSize)
	if err := InitPage(page); err != nil {
		t.Fatal(err)
	}

	// Slot 1: xmin just BEFORE wraparound — logically ancient, numerically huge.
	preWrap := TransactionID(0xFFFFFFF0)
	if _, err := PageAddHeapTuple(page, NewHeapTuple(preWrap, InvalidTransactionID, []byte("old"))); err != nil {
		t.Fatal(err)
	}
	// Slot 2: xmin just AFTER wraparound, newer than the horizon.
	postWrap := TransactionID(200)
	if _, err := PageAddHeapTuple(page, NewHeapTuple(postWrap, InvalidTransactionID, []byte("new"))); err != nil {
		t.Fatal(err)
	}

	stats, err := PageFreezeOldTuples(page, 100) // horizon has wrapped
	if err != nil {
		t.Fatalf("PageFreezeOldTuples: %v", err)
	}
	if stats.Frozen != 1 {
		t.Fatalf("froze %d tuples, want 1 (the pre-wraparound one)", stats.Frozen)
	}
	t1, err := PageGetHeapTuple(page, 1)
	if err != nil {
		t.Fatal(err)
	}
	if t1.Header.Xmin != FrozenTransactionID {
		t.Errorf("slot 1 xmin=%d, want FrozenTransactionID", t1.Header.Xmin)
	}
	if stats.MinUnfrozenXID != postWrap {
		t.Errorf("MinUnfrozenXID=%d, want %d", stats.MinUnfrozenXID, postWrap)
	}
}
