package nbtree

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestUnlinkPinsRefindMovedParent pins latchParentOf, the parent half of
// _bt_lock_subtree_parent: the parent block vacuum resolved before latching
// the siblings is only a hint, because a split of that parent may since have
// moved the downlink right. Given the leftmost parent-level page as the hint
// for a leaf whose downlink lives on the next one, acquireUnlinkPins must
// move right and latch the page that really holds it. A hint that is not on
// the parent level at all leaves the parent unlatched, so the caller abandons
// the deletion instead of retargeting the wrong page.
func TestUnlinkPinsRefindMovedParent(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 256})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	bt, err := Create(pool, storage.RelFileNode{DBOid: 1, RelOid: 9310, Fork: storage.MainFork})
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 3000; n++ {
		key := EncodeVarchar([]byte(fmt.Sprintf("%08d-%0190d", n, n)))
		if err := bt.Insert(key, storage.ItemPointer{Block: storage.BlockNumber(n/100 + 1), Offset: uint16(n%100 + 1)}); err != nil {
			t.Fatal(err)
		}
	}
	first, _, err := bt.leftmostAtLevel(1)
	if err != nil {
		t.Fatal(err)
	}
	s, err := bt.pinR(first)
	if err != nil {
		t.Fatal(err)
	}
	second := readOpaque(s.Page()).Next
	bt.unpinR(s)
	if second == storage.InvalidBlockNumber {
		t.Fatal("the parent level has one page; the test needs two")
	}
	s, err = bt.pinR(second)
	if err != nil {
		t.Fatal(err)
	}
	items, err := bt.format().pageItems(s.Page())
	bt.unpinR(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) < 3 {
		t.Fatalf("parent %d has %d downlinks; the test needs 3", second, len(items))
	}
	leaf := items[1].ptr.Block

	pins, err := bt.acquireUnlinkPins(leaf, first, true)
	if err != nil {
		t.Fatal(err)
	}
	if pins.parent == nil || pins.parentBlk != second {
		t.Errorf("hint %d: parent latched %v at block %d, want block %d", first, pins.parent != nil, pins.parentBlk, second)
	}
	pins.release(bt)

	// A hint on the leaf level is not a parent-level page. The leftmost leaf
	// is far from leaf, so this is the level check, not the guard against
	// latching a held sibling twice.
	leftmost, err := bt.findLeftmostLeaf()
	if err != nil {
		t.Fatal(err)
	}
	pins, err = bt.acquireUnlinkPins(leaf, leftmost, true)
	if err != nil {
		t.Fatal(err)
	}
	if pins.parent != nil {
		t.Errorf("leaf-level hint: parent latched at block %d, want none", pins.parentBlk)
	}
	pins.release(bt)
}
