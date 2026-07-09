package btree

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestApplyParentDownlinkRemovalIgnoresStaleIndex is the regression test for
// the M0122-0010 (AI-20260709-010336-082 follow-up) parent-downlink
// index-drift race: applyParentDownlinkRemoval used to remove-by-INDEX
// against a slot resolved well before the removal actually runs (WAL record
// emission plus sibling-relink writes happen in between, in
// unlinkEmptyLeaf/unlinkEmptyInternalPage). bt.splitMu only serialises
// structural writes within one *BTree Go-instance; each backend opens its
// own instance per statement, so a concurrent Insert-driven split on a
// DIFFERENT connection's instance for the SAME relation can splice a new
// downlink into the parent page ahead of the captured slot, shifting every
// later index right — the stale-index removal would then delete an
// unrelated LIVE child's downlink instead of the intended one, while the
// intended one survives (see the M0122-0010 fix_plan/deferral-ledger entry
// for the full failure narrative).
//
// This test reproduces that window deterministically (no goroutines
// needed): it resolves a target leaf's parent slot, then — simulating a
// same-window concurrent split — splices a brand-new live leaf downlink
// into the FRONT of the parent's item list (shifting the target's true
// position by one), and only THEN invokes applyParentDownlinkRemoval keyed
// on the target's BLOCK. Before the fix (removal by stale index) this
// would have deleted whichever downlink now sits at that index — a
// different, live child (the "victim") — while leaving the intended target
// linked; after the fix, the removal re-locates the target by block
// identity and removes the right one regardless of the shift.
func TestApplyParentDownlinkRemovalIgnoresStaleIndex(t *testing.T) {
	pool, rel := newVacuumTestPool(t)

	const n = 3000
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
	if meta.Level < 1 {
		t.Fatalf("need a >=2-level tree (root downlinks straight to leaves), got level=%d — adjust n", meta.Level)
	}
	rootItems := readItemsForTest(t, tree, meta.Root)
	if len(rootItems) < 4 {
		t.Fatalf("need >=4 root downlinks to pick a non-edge target with a distinct predecessor, got %d", len(rootItems))
	}

	targetIdx := len(rootItems) / 2
	targetBlk := rootItems[targetIdx].ptr.Block
	victimBlk := rootItems[targetIdx-1].ptr.Block // lands at the target's stale slot after the splice below

	// Stale slot a caller would have resolved BEFORE the race (1-based,
	// matching findDownlinkSlotInParent's convention).
	staleSlot, err := tree.findDownlinkSlotInParent(meta.Root, targetBlk)
	if err != nil {
		t.Fatalf("findDownlinkSlotInParent: %v", err)
	}
	if staleSlot != uint16(targetIdx+1) {
		t.Fatalf("staleSlot=%d, want %d", staleSlot, targetIdx+1)
	}

	// Simulate a concurrent Insert-driven split on a DIFFERENT connection's
	// *BTree instance: allocate a brand-new live leaf and splice its
	// downlink into the FRONT of the root's item list, shifting every
	// existing index (including the stale slot above) right by one.
	splicedSlot, splicedBlk, err := pool.PinNew(rel)
	if err != nil {
		t.Fatalf("PinNew spliced leaf: %v", err)
	}
	initPage(splicedSlot.Page(), BTPageOpaque{Level: 0, Flags: BTLeaf})
	pool.Unpin(splicedSlot)

	newItem := item{keyLen: 0, ptr: storage.ItemPointer{Block: splicedBlk, Offset: 0}, key: nil}
	spliced := append([]item{newItem}, rootItems...)

	rootSlot, err := tree.pinW(meta.Root)
	if err != nil {
		t.Fatalf("pinW(root): %v", err)
	}
	resetPageItems(rootSlot.Page())
	for _, it := range spliced {
		if _, err := storage.PageAddItemRaw(rootSlot.Page(), it.marshal()); err != nil {
			t.Fatalf("PageAddItemRaw: %v", err)
		}
	}
	tree.pool.MarkDirty(rootSlot)
	tree.unpinW(rootSlot)

	// staleSlot's 0-based index (targetIdx) now holds victimBlk's downlink,
	// not targetBlk's — confirm the setup actually reproduces the drift
	// before exercising the fix.
	postSplice := readItemsForTest(t, tree, meta.Root)
	if postSplice[targetIdx].ptr.Block != victimBlk {
		t.Fatalf("setup error: post-splice index %d holds block %d, want victim %d", targetIdx, postSplice[targetIdx].ptr.Block, victimBlk)
	}

	// Exercise the fix: remove by BLOCK, not the stale index.
	if err := tree.applyParentDownlinkRemoval(meta.Root, targetBlk, 0); err != nil {
		t.Fatalf("applyParentDownlinkRemoval: %v", err)
	}

	final := readItemsForTest(t, tree, meta.Root)
	if len(final) != len(spliced)-1 {
		t.Fatalf("root has %d items after removal, want %d", len(final), len(spliced)-1)
	}
	var sawVictim, sawSpliced, sawTarget bool
	for _, it := range final {
		switch it.ptr.Block {
		case targetBlk:
			sawTarget = true
		case victimBlk:
			sawVictim = true
		case splicedBlk:
			sawSpliced = true
		}
	}
	if sawTarget {
		t.Fatalf("targetBlk %d downlink still present after removal (should have been removed)", targetBlk)
	}
	if !sawVictim {
		t.Fatalf("victimBlk %d downlink was wrongly removed instead of targetBlk (stale-index regression)", victimBlk)
	}
	if !sawSpliced {
		t.Fatalf("splicedBlk %d downlink was lost", splicedBlk)
	}
}
