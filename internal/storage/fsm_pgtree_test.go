package storage

import "testing"

// TestFSMTreeSearchGoesRightThenWraps is fsmpage.c's worked example for
// fsm_search_avail: leaves 4 5 5 7 2 6 5 2, the hint on the seventh, a request
// for 6. The search moves right from the hint, wraps, and returns the first
// suitable slot to the right of it allowing for wraparound — slot 3 (7), not
// slot 5 (6), which lies left of the hint. The hint then points past slot 3.
func TestFSMTreeSearchGoesRightThenWraps(t *testing.T) {
	p := &fsmTreePage{}
	for slot, v := range []uint8{4, 5, 5, 7, 2, 6, 5, 2} {
		p.setAvail(slot, v)
	}
	p.nextSlot = 6
	if got := p.searchAvail(6, true); got != 3 {
		t.Fatalf("searchAvail(6) from slot 6 = %d, want 3", got)
	}
	if p.nextSlot != 4 {
		t.Fatalf("fp_next_slot = %d, want 4", p.nextSlot)
	}
	// From the hint at 4 the 6 at slot 5 is now to the right.
	if got := p.searchAvail(6, true); got != 5 {
		t.Fatalf("second searchAvail(6) = %d, want 5", got)
	}
	if got := p.searchAvail(8, true); got != -1 {
		t.Fatalf("searchAvail(8) = %d, want -1 (root holds 7)", got)
	}
}

// TestFSMTreeUpperLevelsMoveOnlyOnVacuum pins RecordPageWithFreeSpace's
// caveat: a leaf update is not visible to a search from the root until
// FreeSpaceMapVacuumRange recomputes the upper slots. The range picks which
// upper slots are recomputed — each from its whole leaf page's maximum — and
// its end is exclusive, which matters at a leaf boundary.
func TestFSMTreeUpperLevelsMoveOnlyOnVacuum(t *testing.T) {
	tr := newFSMTree()
	first := BlockNumber(fsmSlotsPerPage) // slot 0 of the second leaf
	tr.record(first, 1000)
	if _, ok := tr.get(500, first+1); ok {
		t.Fatalf("root search found a page no vacuum has propagated")
	}
	tr.vacuumRange(0, first) // [0, first): the second leaf is not visited
	if _, ok := tr.get(500, first+1); ok {
		t.Fatalf("vacuumRange(0, %d) propagated block %d", first, first)
	}
	tr.vacuumRange(0, first+1)
	if blk, ok := tr.get(500, first+1); !ok || blk != first {
		t.Fatalf("after vacuumRange(0, %d) get = %d, %v; want %d", first+1, blk, ok, first)
	}
	// A block past the heap's end is zeroed and not returned.
	if _, ok := tr.get(500, first); ok {
		t.Fatalf("get returned a block beyond the relation")
	}
}

// TestFSMTreeRecordAndGetPrefersTheOldPagesLeaf pins
// RecordAndGetPageWithFreeSpace's preference: a page with room on the old
// page's own FSM leaf wins over a lower-numbered one on another leaf.
func TestFSMTreeRecordAndGetPrefersTheOldPagesLeaf(t *testing.T) {
	tr := newFSMTree()
	low := BlockNumber(5)
	second := BlockNumber(fsmSlotsPerPage + 7)
	tr.record(low, 2000)
	tr.record(second, 2000)
	tr.vacuumRange(0, second+1)
	old := BlockNumber(fsmSlotsPerPage + 100)
	if blk, ok := tr.recordAndGet(old, 0, 1000, old+1); !ok || blk != second {
		t.Fatalf("recordAndGet from block %d = %d, %v; want %d on its own leaf", old, blk, ok, second)
	}
	// With nothing on its own leaf it falls back to the tree.
	tr.record(second, 0)
	if blk, ok := tr.recordAndGet(old, 0, 1000, old+1); !ok || blk != low {
		t.Fatalf("fallback recordAndGet = %d, %v; want %d", blk, ok, low)
	}
}

// TestFSMCategoriesMatchPG pins fsm_space_avail_to_cat / needed_to_cat at the
// edges, with MaxFSMRequestSize = MaxHeapTupleSize = 8160.
func TestFSMCategoriesMatchPG(t *testing.T) {
	for _, c := range []struct {
		avail int
		cat   uint8
	}{{0, 0}, {31, 0}, {32, 1}, {8159, 254}, {8160, 255}, {8164, 255}} {
		if got := fsmAvailToCat(c.avail); got != c.cat {
			t.Errorf("fsmAvailToCat(%d) = %d, want %d", c.avail, got, c.cat)
		}
	}
	for _, c := range []struct {
		need int
		cat  uint8
	}{{0, 1}, {1, 1}, {32, 1}, {33, 2}, {8160, 255}} {
		if got := fsmNeededToCat(c.need); got != c.cat {
			t.Errorf("fsmNeededToCat(%d) = %d, want %d", c.need, got, c.cat)
		}
	}
}
