package storage

import "testing"

// The addressing math is the part of the VM redo helpers that can be wrong
// without anything erroring: a bad byte index or shift silently sets some
// *other* heap block's bit, which shows up much later as an index-only scan
// trusting a page it should have fetched. These cases pin the three boundaries
// where an off-by-one lives: within a byte, at the byte boundary, and at the
// page boundary. M0131-S21a-2 part 3.
func TestVMBitAddressingBoundaries(t *testing.T) {
	page := make(Page, BlockSize)
	if err := InitPage(page); err != nil {
		t.Fatal(err)
	}
	// Four blocks share a byte; the fifth starts the next one; block
	// VMHeapBlocksPerPage belongs to the NEXT vm page and therefore lands back
	// at the start of a page's data area.
	for _, blk := range []BlockNumber{0, 1, 3, 4, VMHeapBlocksPerPage - 1} {
		if _, err := VMPageSetBits(page, blk, VMAllVisible); err != nil {
			t.Fatalf("set %d: %v", blk, err)
		}
	}
	for blk := BlockNumber(0); blk < 8; blk++ {
		want := uint8(0)
		if blk == 0 || blk == 1 || blk == 3 || blk == 4 {
			want = VMAllVisible
		}
		got, err := VMPageBits(page, blk)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("block %d bits = %#x, want %#x", blk, got, want)
		}
	}
	if got := VMBlockForHeapBlock(VMHeapBlocksPerPage); got != 1 {
		t.Fatalf("VMBlockForHeapBlock(%d) = %d, want 1", VMHeapBlocksPerPage, got)
	}
	// The last block of vm page 0 and the first of vm page 1 must not collide
	// in the same page's data area.
	first, err := VMPageBits(page, VMHeapBlocksPerPage)
	if err != nil {
		t.Fatal(err)
	}
	if first != VMAllVisible {
		// Same slot as block 0 within a page — that is correct addressing, and
		// asserting it here is what proves the modulo is present at all.
		t.Fatalf("wrapped block bits = %#x, want the same slot as block 0", first)
	}
}

// VMPageSetBits mirrors visibilitymap_set's "flags != status" short-circuit,
// which is what lets redo skip the page write (and the pd_lsn stamp) on a
// record that changes nothing.
func TestVMPageSetBitsReportsNoChange(t *testing.T) {
	page := make(Page, BlockSize)
	if err := InitPage(page); err != nil {
		t.Fatal(err)
	}
	changed, err := VMPageSetBits(page, 5, VMAllVisible)
	if err != nil || !changed {
		t.Fatalf("first set: changed=%v err=%v, want changed", changed, err)
	}
	changed, err = VMPageSetBits(page, 5, VMAllVisible)
	if err != nil || changed {
		t.Fatalf("repeat set: changed=%v err=%v, want no change", changed, err)
	}
	if _, err := VMPageSetBits(page, 5, VMAllVisible|0x04); err == nil {
		t.Fatal("set with VISIBILITYMAP_XLOG_CATALOG_REL must be refused — that bit is wire-only")
	}
}

// Upstream asserts a caller never clears ALL_VISIBLE while leaving ALL_FROZEN
// set: an all-frozen-but-not-all-visible page is a corrupt map state. goopg
// enforces it instead of asserting, because the caller here is a WAL record.
func TestVMPageClearBitsRejectsAllVisibleAlone(t *testing.T) {
	page := make(Page, BlockSize)
	if err := InitPage(page); err != nil {
		t.Fatal(err)
	}
	if _, err := VMPageSetBits(page, 2, VMAllVisible|VMAllFrozen); err != nil {
		t.Fatal(err)
	}
	if _, err := VMPageClearBits(page, 2, VMAllVisible); err == nil {
		t.Fatal("clearing ALL_VISIBLE alone must be refused")
	}
	changed, err := VMPageClearBits(page, 2, VMAllFrozen)
	if err != nil || !changed {
		t.Fatalf("clear ALL_FROZEN: changed=%v err=%v", changed, err)
	}
	got, err := VMPageBits(page, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got != VMAllVisible {
		t.Fatalf("bits = %#x, want ALL_VISIBLE only", got)
	}
	if changed, err := VMPageClearBits(page, 2, VMAllFrozen); err != nil || changed {
		t.Fatalf("repeat clear: changed=%v err=%v, want no change", changed, err)
	}
}

// The redo helpers and the fork writer must agree on the layout, or bits
// written by recovery would read back shifted once the server loads the fork.
func TestVMRedoBitsRoundTripThroughForkParser(t *testing.T) {
	page := make(Page, BlockSize)
	if err := InitPage(page); err != nil {
		t.Fatal(err)
	}
	for _, blk := range []BlockNumber{0, 2, 9, 100} {
		if _, err := VMPageSetBits(page, blk, VMAllVisible); err != nil {
			t.Fatal(err)
		}
	}
	masks := parseVMPage(page)
	for _, blk := range []BlockNumber{0, 2, 9, 100} {
		if masks[blk]&VMAllVisible == 0 {
			t.Fatalf("parseVMPage lost block %d — redo and the fork writer disagree on layout", blk)
		}
	}
	if masks[1] != 0 || masks[3] != 0 || masks[99] != 0 {
		t.Fatal("parseVMPage sees bits redo never set")
	}
}
