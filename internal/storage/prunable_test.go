package storage

import "testing"

// TestProducersKeepOldestPruneXID pins PageSetPrunable (bufpage.h) at the
// producers (M0145-0008x): pd_prune_xid is only ever lowered, so a page that
// keeps being updated still carries the xid that first made it prunable and
// passes the prune gate once that xid is below the horizon. The comparison is
// wraparound-safe.
func TestProducersKeepOldestPruneXID(t *testing.T) {
	p := make(Page, BlockSize)
	if err := InitPage(p); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if _, err := PageAddHeapTuple(p, NewHeapTuple(3, InvalidTransactionID, []byte("row"))); err != nil {
			t.Fatal(err)
		}
	}
	stamp := func(slot uint16, xmax TransactionID) {
		t.Helper()
		if err := PageSetHeapTupleXmax(p, slot, xmax); err != nil {
			t.Fatal(err)
		}
	}
	stamp(1, 100)
	stamp(2, 200) // a newer deleter must not raise the hint
	if got := TransactionID(MustHeader(p).PruneXID()); got != 100 {
		t.Fatalf("pd_prune_xid = %d after 100 then 200, want 100 (the oldest)", got)
	}
	stamp(3, 50)
	if got := TransactionID(MustHeader(p).PruneXID()); got != 50 {
		t.Fatalf("pd_prune_xid = %d after an older deleter, want 50", got)
	}
	// With the hint at the oldest xid, the prune gate opens as soon as that
	// xid is below the horizon, however many newer updates followed.
	if !XIDPrecedes(TransactionID(MustHeader(p).PruneXID()), 150) {
		t.Fatal("the hint must precede a horizon past the oldest deleter")
	}

	// Wraparound: an xid just past the wrap point is NEWER than one just
	// before it, so it must not replace it.
	q := make(Page, BlockSize)
	if err := InitPage(q); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := PageAddHeapTuple(q, NewHeapTuple(3, InvalidTransactionID, []byte("row"))); err != nil {
			t.Fatal(err)
		}
	}
	old := TransactionID(0xFFFFFFF0)
	if err := PageSetHeapTupleXmax(q, 1, old); err != nil {
		t.Fatal(err)
	}
	if err := PageSetHeapTupleXmax(q, 2, 5); err != nil { // wrapped: newer than 0xFFFFFFF0
		t.Fatal(err)
	}
	if got := TransactionID(MustHeader(q).PruneXID()); got != old {
		t.Fatalf("pd_prune_xid = %#x across the wrap, want %#x", got, old)
	}
}
