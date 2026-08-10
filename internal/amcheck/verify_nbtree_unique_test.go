package amcheck

// Unit tests for the checkunique tier (verify_nbtree_unique.go). They exercise
// the tier's defining distinction: a unique index may hold many entries for one
// key (dead row versions awaiting VACUUM), and only a pair whose heap tuples are
// *simultaneously visible* is a violation. Each test therefore pairs a page
// layout with a visibility oracle.

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/storage"
)

// visibleTIDs builds a HeapVisibilityFunc that reports true for exactly the
// listed (block, offset) pairs.
func visibleTIDs(tids ...storage.ItemPointer) HeapVisibilityFunc {
	set := make(map[storage.ItemPointer]bool, len(tids))
	for _, t := range tids {
		set[t] = true
	}
	return func(tid storage.ItemPointer) bool { return set[tid] }
}

func allVisible() HeapVisibilityFunc {
	return func(storage.ItemPointer) bool { return true }
}

// A unique index with distinct keys is clean however visible its heap is — the
// no-false-positive gate for the tier.
func TestVerifyBtreeUnique_DistinctKeysClean(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{
		btree.MetaBlock: makeMetaWithRoot(t, 1),
		1: makeLeafPage(t, none,
			le{k(1), 10, 1},
			le{k(2), 10, 2},
			le{k(3), 10, 3}),
	}
	got, err := VerifyBtreeUnique(mapSource(pages), "uidx", nil, allVisible())
	if err != nil {
		t.Fatalf("VerifyBtreeUnique: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("clean unique index reported %d findings: %+v", len(got), got)
	}
}

// Two entries for the same key whose heap tuples are BOTH visible is the
// violation upstream's bt_entry_unique_check reports.
func TestVerifyBtreeUnique_DuplicateBothVisible(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{
		btree.MetaBlock: makeMetaWithRoot(t, 1),
		1: makeLeafPage(t, none,
			le{k(1), 10, 1},
			le{k(1), 10, 7},
			le{k(3), 10, 3}),
	}
	got, err := VerifyBtreeUnique(mapSource(pages), "uidx", nil, allVisible())
	if err != nil {
		t.Fatalf("VerifyBtreeUnique: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	if want := `index uniqueness is violated for index "uidx"`; got[0].Msg != want {
		t.Fatalf("Msg = %q, want %q", got[0].Msg, want)
	}
	// The detail must name both index locations and both heap TIDs, the way
	// upstream's bt_report_duplicate errdetail does.
	for _, sub := range []string{"Index tid=(1,1)", "tid=(1,2)", "heap tid=(10,1)", "tid=(10,7)"} {
		if !strings.Contains(got[0].Detail, sub) {
			t.Errorf("Detail %q lacks %q", got[0].Detail, sub)
		}
	}
}

// The same duplicate key is CLEAN when only one of the two heap tuples is
// visible: that is an ordinary dead row version still referenced by the index,
// which every unique index accumulates between VACUUMs. Getting this wrong
// would make bt_index_check --checkunique fire on healthy tables.
func TestVerifyBtreeUnique_DuplicateOneVisibleClean(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{
		btree.MetaBlock: makeMetaWithRoot(t, 1),
		1: makeLeafPage(t, none,
			le{k(1), 10, 1},
			le{k(1), 10, 7}),
	}
	got, err := VerifyBtreeUnique(mapSource(pages), "uidx", nil,
		visibleTIDs(storage.ItemPointer{Block: 10, Offset: 7}))
	if err != nil {
		t.Fatalf("VerifyBtreeUnique: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("dead-version duplicate reported %d findings: %+v", len(got), got)
	}
}

// A duplicate split across a leaf-page boundary must be found: the last entry of
// one leaf versus the first of its right sibling. This is the cross-page carry
// of upstream's BtreeLastVisibleEntry.
func TestVerifyBtreeUnique_DuplicateAcrossLeafBoundary(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{
		btree.MetaBlock: makeMetaWithRoot(t, 1),
		1: makeInternalPage(t, 1, none,
			dl{nil, 2},
			dl{k(5), 3}),
		2: makeLeafPage(t, 3, le{k(1), 20, 1}, le{k(5), 20, 2}),
		3: makeLeafPage(t, none, le{k(5), 21, 1}, le{k(7), 21, 2}),
	}
	got, err := VerifyBtreeUnique(mapSource(pages), "uidx", nil, allVisible())
	if err != nil {
		t.Fatalf("VerifyBtreeUnique: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	if got[0].Block != 3 {
		t.Fatalf("finding Block = %d, want 3 (the right sibling)", got[0].Block)
	}
	// Both index locations are on different pages, so both tids are printed.
	// Offsets are PHYSICAL (upstream's errdetail spelling): page 2 is
	// non-rightmost, so P_HIKEY owns offset 1 and its second entry sits at 3.
	if !strings.Contains(got[0].Detail, "Index tid=(2,3)") ||
		!strings.Contains(got[0].Detail, "tid=(3,1)") {
		t.Errorf("Detail %q does not name both cross-page index tids", got[0].Detail)
	}
}

// An intervening distinct key resets the run: entries with key 1 on either side
// of a key-2 entry cannot both be "the same key", so nothing is reported even
// though every heap tuple is visible. (Such a layout is an item-order violation,
// which is a different tier's finding — this tier must not double-report it.)
func TestVerifyBtreeUnique_RunResetsOnKeyChange(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{
		btree.MetaBlock: makeMetaWithRoot(t, 1),
		1: makeLeafPage(t, none,
			le{k(1), 10, 1},
			le{k(2), 10, 2},
			le{k(1), 10, 3}),
	}
	got, err := VerifyBtreeUnique(mapSource(pages), "uidx", nil, allVisible())
	if err != nil {
		t.Fatalf("VerifyBtreeUnique: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("non-adjacent equal keys reported %d findings: %+v", len(got), got)
	}
}

// The comparator seam decides what "the same key" means: under a comparator that
// declares every key equal, two distinct-key entries become a duplicate. This is
// the same KeyComparator the operator-class dispatch injects, so a user opclass
// governs the uniqueness tier exactly as it governs item order.
func TestVerifyBtreeUnique_HonoursKeyComparator(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{
		btree.MetaBlock: makeMetaWithRoot(t, 1),
		1: makeLeafPage(t, none,
			le{k(1), 10, 1},
			le{k(2), 10, 2}),
	}
	allEqual := func(a, b []byte) int { return 0 }
	got, err := VerifyBtreeUnique(mapSource(pages), "uidx", allEqual, allVisible())
	if err != nil {
		t.Fatalf("VerifyBtreeUnique: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1 under an all-equal comparator: %+v", len(got), got)
	}
}

// An index with no key level (metapage only) is vacuously unique, and a missing
// visibility function is a caller error rather than a silent pass.
func TestVerifyBtreeUnique_EdgeCases(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{
		btree.MetaBlock: makeMetaPage(t, btree.BTreeMagic, btree.BTreeVersion),
	}
	got, err := VerifyBtreeUnique(mapSource(pages), "uidx", nil, allVisible())
	if err != nil || len(got) != 0 {
		t.Fatalf("empty tree: got %d findings, err %v; want 0, nil", len(got), err)
	}
	if _, err := VerifyBtreeUnique(mapSource(pages), "uidx", nil, nil); err == nil {
		t.Fatal("nil visibility function accepted; want an error")
	}
}

// PageLeafItems must agree with PageLeafEntries on which entries a page yields
// (PageLeafEntries is implemented as a projection of it) and additionally report
// each entry's slot — the coordinates the duplicate detail prints.
func TestPageLeafItemsMatchesLeafEntries(t *testing.T) {
	p := makeLeafPage(t, none, le{k(1), 10, 1}, le{k(2), 10, 2}, le{k(3), 11, 5})
	items, err := btree.PageLeafItems(p)
	if err != nil {
		t.Fatalf("PageLeafItems: %v", err)
	}
	entries, err := btree.PageLeafEntries(p)
	if err != nil {
		t.Fatalf("PageLeafEntries: %v", err)
	}
	if len(items) != len(entries) {
		t.Fatalf("PageLeafItems yielded %d, PageLeafEntries %d", len(items), len(entries))
	}
	for i := range items {
		if items[i].TID != entries[i].TID || string(items[i].Key) != string(entries[i].Key) {
			t.Errorf("entry %d disagrees: %+v vs %+v", i, items[i], entries[i])
		}
		if items[i].Slot != uint16(i+1) {
			t.Errorf("entry %d Slot = %d, want %d", i, items[i].Slot, i+1)
		}
		if items[i].PostingIndex != -1 {
			t.Errorf("entry %d PostingIndex = %d, want -1 (plain item)", i, items[i].PostingIndex)
		}
	}
}
