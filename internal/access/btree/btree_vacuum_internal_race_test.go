package btree

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestUnlinkEmptyInternalPagePreservesConcurrentSplice is the regression
// test for the M-NIGHTLY (AI-20260709-010336-082 follow-up) internal-page
// sibling-relink race: unlinkEmptyInternalPage's WAL path computed
// leftLive/rightLive via an unlocked liveSibling pre-pass and then wrote
// those captured values verbatim into the sibling pages — exactly the same
// stale-capture bug already fixed for LEAF pages in unlinkEmptyLeaf (see
// that function's doc comment), just one level up.
//
// bt.splitMu only serialises structural writes within one *BTree
// Go-instance; each backend opens its own instance per statement, so it
// does NOT prevent a concurrent Insert-driven split on a DIFFERENT
// connection's instance for the SAME relation from splicing a brand-new
// live internal page into the exact chain segment between
// maybeCascadeEmptyInternal's `prev, next := op.Prev, op.Next` capture
// (btree_vacuum.go) and unlinkEmptyInternalPage's sibling-relink writes.
//
// This test reproduces that window deterministically (no goroutines
// needed): it captures a real internal page's live prev/next exactly like
// maybeCascadeEmptyInternal does, then — simulating a same-window
// concurrent split — splices a brand-new live page in between the left
// sibling and the target, and only THEN invokes the low-level unlink
// function with the stale (pre-splice) prev/next. Before the fix, the
// blind stomp `op.Next = req.LeftSibNewNext` would have overwritten the
// splice and orphaned the new page; after the fix, the write re-derives
// the live neighbour fresh under pinW and leaves the splice intact.
func TestUnlinkEmptyInternalPagePreservesConcurrentSplice(t *testing.T) {
	pool, rel := newVacuumTestPool(t)

	// Same n=900000 recipe as TestVacuumIndexPagesCascadesEmptyInternalPage
	// — empirically the smallest round number that reliably yields a
	// level>=2 tree (root -> internal -> leaf) with >=4 root downlinks, so
	// a middle root child has live internal-level siblings on both sides.
	const n = 900000
	entries := make([]BulkEntry, n)
	for i := range n {
		entries[i] = BulkEntry{
			Key: EncodeInt4(int32(i)),
			Ptr: storage.ItemPointer{Block: storage.BlockNumber(i / 100), Offset: uint16(i%100 + 1)},
		}
	}
	tree, err := BulkCreate(pool, rel, entries)
	if err != nil {
		t.Fatalf("BulkCreate: %v", err)
	}

	meta, err := tree.readMeta()
	if err != nil {
		t.Fatalf("readMeta: %v", err)
	}
	if meta.Level < 2 {
		t.Fatalf("need a 3-level tree (root level>=2), got level=%d — adjust n", meta.Level)
	}
	rootItems := readItemsForTest(t, tree, meta.Root)
	if len(rootItems) < 3 {
		t.Fatalf("need >=3 root downlinks to pick a non-edge internal child, got %d", len(rootItems))
	}
	targetInternal := rootItems[len(rootItems)/2].ptr.Block

	// Capture the target's live siblings exactly like
	// maybeCascadeEmptyInternal does, BEFORE any racing mutation.
	targetOp := vacOpaque(t, tree, targetInternal)
	leftSib, rightSib := targetOp.Prev, targetOp.Next
	if leftSib == storage.InvalidBlockNumber || rightSib == storage.InvalidBlockNumber {
		t.Fatalf("target internal page %d needs live siblings on both sides (prev=%d next=%d)", targetInternal, leftSib, rightSib)
	}

	// Simulate a concurrent Insert-driven split on a DIFFERENT connection's
	// *BTree instance: it allocates a brand-new live page and splices it
	// in between leftSib and targetInternal, in the window after VACUUM's
	// capture above but before the unlink write below. A bare internal
	// opaque header (any non-deleted/non-half-dead level marker) is
	// sufficient — liveSibling only inspects BTDeleted/BTHalfDead.
	splicedSlot, splicedBlk, err := pool.PinNew(rel)
	if err != nil {
		t.Fatalf("PinNew spliced page: %v", err)
	}
	initPage(splicedSlot.Page(), BTPageOpaque{
		Level: targetOp.Level,
		Flags: 0,
		Prev:  leftSib,
		Next:  targetInternal,
	})
	pool.Unpin(splicedSlot)

	// The racing split's OWN relink is correct in isolation: it re-read
	// leftSib fresh under its own pinW immediately before writing.
	leftSlot, err := tree.pinW(leftSib)
	if err != nil {
		t.Fatalf("pinW(leftSib=%d): %v", leftSib, err)
	}
	leftOp := readOpaque(leftSlot.Page())
	if leftOp.Next != targetInternal {
		tree.unpinW(leftSlot)
		t.Fatalf("leftSib %d Next=%d, want targetInternal=%d before splice", leftSib, leftOp.Next, targetInternal)
	}
	leftOp.Next = splicedBlk
	writeOpaque(leftSlot.Page(), leftOp)
	tree.unpinW(leftSlot)

	// Now run the unlink with the STALE prev/next captured before the
	// splice — exactly what maybeCascadeEmptyInternal's real call site
	// passes when a concurrent split has raced ahead of it.
	if err := tree.unlinkEmptyInternalPage(targetInternal, leftSib, rightSib, meta.Root); err != nil {
		t.Fatalf("unlinkEmptyInternalPage: %v", err)
	}

	// The fix: leftSib's Next must still be the spliced page, not
	// stomped back to rightSib. Pre-fix, this would be rightSib.
	gotLeftNext := vacOpaque(t, tree, leftSib).Next
	if gotLeftNext != splicedBlk {
		t.Fatalf("leftSib %d Next=%d after unlink, want spliced page %d preserved (stale stomp regression)", leftSib, gotLeftNext, splicedBlk)
	}

	// The spliced page's own links must be untouched by the unlink.
	splicedOp := vacOpaque(t, tree, splicedBlk)
	if splicedOp.Prev != leftSib || splicedOp.Next != targetInternal {
		t.Fatalf("spliced page %d links corrupted by unlink: prev=%d next=%d, want prev=%d next=%d", splicedBlk, splicedOp.Prev, splicedOp.Next, leftSib, targetInternal)
	}

	// The target must be fully unlinked from the parent regardless.
	if !vacOpaque(t, tree, targetInternal).IsDeleted() {
		t.Fatalf("target internal page %d was expected deleted", targetInternal)
	}
	for _, it := range readItemsForTest(t, tree, meta.Root) {
		if it.ptr.Block == targetInternal {
			t.Fatalf("root still downlinks to unlinked internal page %d", targetInternal)
		}
	}
}
