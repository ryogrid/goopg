package storage

import "testing"

// TestPageAllVisibleClearedByHeapChanges pins M0122-0012 C47's invalidation
// rule at the storage layer, where every heap tuple add and xmax stamp lands.
// A seq scan skips per-tuple visibility on a page with PD_ALL_VISIBLE, so any
// change that can make a tuple on the page invisible to some snapshot must
// clear it: an insert (heap_insert, heap_update's new page), a delete or
// update stamp (heap_delete / heap_update, HOT or not, single xid or a multi
// with an updater) and a cross-partition move. A row lock keeps it, as
// heap_lock_tuple does.
func TestPageAllVisibleClearedByHeapChanges(t *testing.T) {
	newPage := func(t *testing.T) Page {
		t.Helper()
		p := make(Page, BlockSize)
		if err := InitPage(p); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 3; i++ {
			if _, err := PageAddHeapTuple(p, NewHeapTuple(TransactionID(100+i), InvalidTransactionID, []byte("row"))); err != nil {
				t.Fatal(err)
			}
		}
		PageSetAllVisible(p)
		if !PageIsAllVisible(p) {
			t.Fatal("PageSetAllVisible did not set the bit")
		}
		return p
	}
	for _, tc := range []struct {
		name    string
		change  func(Page) error
		cleared bool
	}{
		{"insert", func(p Page) error {
			_, err := PageAddHeapTuple(p, NewHeapTuple(TransactionID(200), InvalidTransactionID, []byte("new")))
			return err
		}, true},
		{"delete xmax", func(p Page) error { return PageSetHeapTupleXmax(p, 1, 300) }, true},
		{"moved partition", func(p Page) error { return PageSetHeapTupleMovedPartition(p, 1, 300) }, true},
		{"hot update old", func(p Page) error { return PageStampHotOldTuple(p, 1, 300, 0, 2) }, true},
		{"update old", func(p Page) error { return PageStampUpdatedOldTuple(p, 1, 300, 0, 2) }, true},
		{"multi with updater", func(p Page) error { return PageSetHeapTupleXmaxMulti(p, 1, 5, HeapXmaxIsMulti, 0) }, true},
		{"lock-only multi", func(p Page) error {
			return PageSetHeapTupleXmaxMulti(p, 1, 5, HeapXmaxIsMulti|HeapXmaxLockOnly, 0)
		}, false},
		{"row lock", func(p Page) error { return PageSetHeapTupleLockOnly(p, 1, 300, HeapXmaxExclLock) }, false},
	} {
		p := newPage(t)
		if err := tc.change(p); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := !PageIsAllVisible(p); got != tc.cleared {
			t.Errorf("%s: PD_ALL_VISIBLE cleared = %v, want %v", tc.name, got, tc.cleared)
		}
	}
	p := newPage(t)
	if !PageClearAllVisible(p) || PageIsAllVisible(p) || PageClearAllVisible(p) {
		t.Fatal("PageClearAllVisible must clear once and report whether the bit was set")
	}
}
