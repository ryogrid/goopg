package storage

import "testing"

// TestPagePruneOnAccessWantedGate pins heap_page_prune_opt's gate
// (M0145-0008t): a set pd_prune_xid older than the horizon, and free space
// below Max(BLCKSZ*(100-fillfactor)/100, BLCKSZ/10).
func TestPagePruneOnAccessWantedGate(t *testing.T) {
	p := make(Page, BlockSize)
	if err := InitPage(p); err != nil {
		t.Fatal(err)
	}
	h := MustHeader(p)
	if PagePruneXIDSet(p) || PagePruneOnAccessWanted(p, 100, 100) {
		t.Fatal("an unhinted page must not be pruned")
	}
	h.SetPruneXID(50)
	// An empty page has plenty of room at fillfactor 100 (minfree 819).
	if PagePruneOnAccessWanted(p, 100, 100) {
		t.Fatal("a page with room to spare must not be pruned")
	}
	// Squeeze the free space to ~1000 bytes: above BLCKSZ/10, below the
	// fillfactor-50 target of BLCKSZ/2.
	h.SetUpper(h.Lower() + 1000)
	if PagePruneOnAccessWanted(p, 100, 100) {
		t.Fatal("fillfactor 100: 1000 bytes free is above BLCKSZ/10")
	}
	if !PagePruneOnAccessWanted(p, 100, 50) {
		t.Fatal("fillfactor 50: 1000 bytes free is below the target")
	}
	// The hint must precede the horizon.
	if PagePruneOnAccessWanted(p, 50, 50) || PagePruneOnAccessWanted(p, InvalidTransactionID, 50) {
		t.Fatal("a hint not older than the horizon must not prune")
	}
	h.SetUpper(h.Lower() + 100)
	if !PagePruneOnAccessWanted(p, 100, 100) {
		t.Fatal("100 bytes free is below BLCKSZ/10")
	}
}
