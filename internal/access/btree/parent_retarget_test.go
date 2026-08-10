package btree

import (
	"errors"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// Guards for M0130-S11.5d-3a: the parent limb of page deletion is now
// upstream's retarget-and-delete, located by CHILD BLOCK, and shared verbatim
// between the primary (applyParentDownlinkRemoval / removeDownlinkFromParent)
// and redo (replayBtreeUnlinkPage's parent limb). The three properties below
// are what make that sharing safe; each of them is a way the two sides could
// silently drift apart.

// TestReplayParentRetargetByChildLocatesByIdentity pins the lookup: the caller
// never supplies an offset, so a downlink that moved (a concurrent split
// splicing an item in ahead of it — M0122-0010) is still found and still the
// one mutated. Both page shapes are covered because P_FIRSTDATAKEY differs
// between them, which is exactly the conversion an offset-carrying record got
// wrong.
func TestReplayParentRetargetByChildLocatesByIdentity(t *testing.T) {
	for _, tc := range []struct {
		name       string
		hasHighKey bool
	}{
		{"non-rightmost parent (P_FIRSTDATAKEY=2)", true},
		{"rightmost parent (P_FIRSTDATAKEY=1)", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page := halfDeadParentPage(t, tc.hasHighKey, []storage.BlockNumber{5, 7, 8})
			// Delete the MIDDLE child: its own item adopts 8, and 8's item goes.
			if err := ReplayParentRetargetByChild(page, 7); err != nil {
				t.Fatal(err)
			}
			got := halfDeadDownlinks(t, page)
			want := []storage.BlockNumber{5, 8}
			if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
				t.Fatalf("downlinks = %v, want %v", got, want)
			}
		})
	}
}

// TestReplayParentRetargetByChildRefusesRightmostChild pins upstream's one
// structural refusal (`_bt_lock_subtree_parent`): the last item has no right
// neighbour to absorb the key range, so the deletion cannot proceed. This is
// also what keeps an internal page from ever reaching zero items via a downlink
// removal — the empty-internal-page hazard of AI-20260706-201855-001.
func TestReplayParentRetargetByChildRefusesRightmostChild(t *testing.T) {
	page := halfDeadParentPage(t, true, []storage.BlockNumber{5, 7, 8})
	err := ReplayParentRetargetByChild(page, 8)
	if !errors.Is(err, ErrParentRightmostChild) {
		t.Fatalf("err = %v, want ErrParentRightmostChild", err)
	}
	// The refusal must be a pure test: the page is untouched.
	got := halfDeadDownlinks(t, page)
	want := []storage.BlockNumber{5, 7, 8}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("downlinks = %v, want %v (page must not be mutated)", got, want)
		}
	}
}

// TestReplayParentRetargetByChildMissingIsNoOp is the idempotency contract redo
// depends on: a record whose parent limb already applied finds no downlink for
// the child, and must leave the page exactly as it is rather than mutating some
// other item or reporting an error that would abort recovery.
func TestReplayParentRetargetByChildMissingIsNoOp(t *testing.T) {
	page := halfDeadParentPage(t, true, []storage.BlockNumber{5, 7, 8})
	if err := ReplayParentRetargetByChild(page, 4242); err != nil {
		t.Fatalf("missing downlink should be a no-op, got %v", err)
	}
	got := halfDeadDownlinks(t, page)
	want := []storage.BlockNumber{5, 7, 8}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("downlinks = %v, want %v", got, want)
		}
	}
}

// TestVacuumLeavesRightmostChildLeafLive drives the refusal end to end through
// the real VacuumIndexPages path: emptying the leaf that is its parent's LAST
// child must leave it EMPTY BUT LIVE — no BTHalfDead, no BTDeleted, downlink
// intact — which is what upstream's `_bt_pagedel` does when
// `_bt_lock_subtree_parent` says no. The flags matter as much as the downlink:
// a leaf left marked while its parent still points at it is invisible to
// liveSibling and eligible for recycleBlock, i.e. a block handed to an
// unrelated split while it is still reachable from the tree.
func TestVacuumLeavesRightmostChildLeafLive(t *testing.T) {
	pool, rel := newVacuumTestPool(t)

	const n = 200000
	entries := make([]BulkEntry, n)
	for i := range entries {
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
		t.Fatalf("need a multi-level tree, got level=%d", meta.Level)
	}

	// Descend always taking the LAST downlink: the leaf we land on is the
	// rightmost child of its parent, the case upstream refuses.
	blk := meta.Root
	parent := storage.InvalidBlockNumber
	for !vacOpaque(t, tree, blk).IsLeaf() {
		items := readItemsForTest(t, tree, blk)
		if len(items) == 0 {
			t.Fatalf("internal page %d is empty", blk)
		}
		parent, blk = blk, items[len(items)-1].ptr.Block
	}
	if parent == storage.InvalidBlockNumber {
		t.Fatal("single-page tree: no parent to refuse against")
	}

	var deadTIDs []storage.ItemPointer
	for _, it := range readItemsForTest(t, tree, blk) {
		deadTIDs = append(deadTIDs, it.ptr)
	}
	if len(deadTIDs) == 0 {
		t.Fatalf("rightmost leaf %d is already empty", blk)
	}
	if removed, err := tree.VacuumIndexPages(deadTIDs); err != nil {
		t.Fatalf("VacuumIndexPages: %v", err)
	} else if removed != len(deadTIDs) {
		t.Fatalf("removed = %d, want %d", removed, len(deadTIDs))
	}

	op := vacOpaque(t, tree, blk)
	if op.IsDeleted() || op.IsHalfDead() {
		t.Fatalf("rightmost-child leaf %d must stay live, flags=%#x", blk, op.Flags)
	}
	count, err := PGDataItemCount(pageOf(t, tree, blk))
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("leaf %d should be empty after vacuum, has %d items", blk, count)
	}
	var stillLinked bool
	for _, it := range readItemsForTest(t, tree, parent) {
		if it.ptr.Block == blk {
			stillLinked = true
		}
	}
	if !stillLinked {
		t.Fatalf("parent %d lost its downlink to the still-live leaf %d", parent, blk)
	}

	// The tree must remain fully readable and writable afterwards.
	want := n - len(deadTIDs)
	var got int
	if err := tree.RangeScan(nil, nil, func(_ []byte, _ storage.ItemPointer) (bool, error) {
		got++
		return true, nil
	}); err != nil {
		t.Fatalf("RangeScan: %v", err)
	}
	if got != want {
		t.Fatalf("surviving entries = %d, want %d", got, want)
	}
	if err := tree.Insert(EncodeInt4(int32(n+1)), storage.ItemPointer{Block: 0, Offset: 1}); err != nil {
		t.Fatalf("Insert after refused deletion: %v", err)
	}
}

// pageOf returns a copy of blk's page bytes under a shared latch.
func pageOf(t *testing.T, bt *BTree, blk storage.BlockNumber) storage.Page {
	t.Helper()
	slot, err := bt.pinR(blk)
	if err != nil {
		t.Fatalf("pinR(%d): %v", blk, err)
	}
	out := make(storage.Page, storage.BlockSize)
	copy(out, slot.Page())
	bt.unpinR(slot)
	return out
}

// TestPGFindDownlinkOffsetReportsPhysicalOffset pins the coordinate system:
// the returned offset is the PHYSICAL OffsetNumber ReplayHalfDeadParent (and
// the PG record's `poffset` field) consumes, not a data-slot index. On a page
// with a high key the two differ by one, and confusing them silently retargets
// the neighbouring child.
func TestPGFindDownlinkOffsetReportsPhysicalOffset(t *testing.T) {
	for _, tc := range []struct {
		name       string
		hasHighKey bool
		want       uint16
	}{
		{"with high key", true, 3},
		{"rightmost, no high key", false, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page := halfDeadParentPage(t, tc.hasHighKey, []storage.BlockNumber{5, 7, 8})
			off, isLast, ok, err := PGFindDownlinkOffset(page, 7)
			if err != nil || !ok {
				t.Fatalf("PGFindDownlinkOffset: ok=%v err=%v", ok, err)
			}
			if off != tc.want {
				t.Errorf("offset = %d, want %d", off, tc.want)
			}
			if isLast {
				t.Errorf("child 7 is not the last item")
			}
			raw, err := storage.PageGetItemRaw(page, off)
			if err != nil {
				t.Fatal(err)
			}
			if BTreeTupleGetDownLink(raw) != 7 {
				t.Errorf("offset %d does not hold child 7", off)
			}
		})
	}
}
