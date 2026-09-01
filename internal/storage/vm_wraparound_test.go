package storage

import "testing"

// TestPageAllVisibleAcrossWraparound is the review/260831-2 ST-7 guard.
// PageAllVisible / PageAllFrozen compared xmin against the horizon with a
// plain unsigned `>=`, so after the XID counter wrapped, a page holding only
// ancient (numerically huge) xmins was judged "newer than the horizon" and
// could never be marked all-visible or all-frozen again. PG compares
// circularly (TransactionIdPrecedes, heap_page_is_all_visible).
func TestPageAllVisibleAcrossWraparound(t *testing.T) {
	page := make(Page, BlockSize)
	if err := InitPage(page); err != nil {
		t.Fatal(err)
	}
	preWrap := TransactionID(0xFFFFFFF0) // committed just before wraparound
	if _, err := PageAddHeapTuple(page, NewHeapTuple(preWrap, InvalidTransactionID, []byte("old"))); err != nil {
		t.Fatal(err)
	}

	horizon := TransactionID(100) // the counter has wrapped past 2^32
	if !PageAllVisible(page, horizon) {
		t.Error("PageAllVisible = false for a page whose only xmin precedes the wrapped horizon")
	}
	if !PageAllFrozen(page, horizon) {
		t.Error("PageAllFrozen = false for a page whose only xmin precedes the wrapped horizon")
	}
}
