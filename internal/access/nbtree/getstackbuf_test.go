package nbtree

import (
	"fmt"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestGetStackBuf pins getStackBuf (_bt_getstackbuf): the parent of a split
// page is re-found by its downlink, not trusted from the descent's path —
// from an empty path (the tree grew a level above it) via the level's
// leftmost page, from a stale hint left of the real parent by moving right,
// and from a hint on the wrong level by starting over. A page with no
// downlink is PG's "failed to re-find parent key" error.
func TestGetStackBuf(t *testing.T) {
	bt, _, cleanup := newTestTree(t)
	defer cleanup()
	for n := 0; n < 4000; n++ {
		key := EncodeVarchar([]byte(fmt.Sprintf("%08d-%0190d", n, n)))
		if err := bt.Insert(key, storage.ItemPointer{Block: storage.BlockNumber(n/100 + 1), Offset: uint16(n%100 + 1)}); err != nil {
			t.Fatal(err)
		}
	}
	meta, err := bt.readMeta()
	if err != nil {
		t.Fatal(err)
	}
	if meta.Level < 2 {
		t.Fatalf("tree level %d, want >= 2 for a parent level with several pages", meta.Level)
	}
	// The leftmost level-1 page, and a leaf whose parent is further right.
	leftParent, _, err := bt.leftmostAtLevel(1)
	if err != nil {
		t.Fatal(err)
	}
	var child, trueParent storage.BlockNumber
	for blk := leftParent; blk != storage.InvalidBlockNumber; {
		slot, err := bt.pinR(blk)
		if err != nil {
			t.Fatal(err)
		}
		op := readOpaque(slot.Page())
		items, err := bt.format().pageItems(slot.Page())
		bt.unpinR(slot)
		if err != nil {
			t.Fatal(err)
		}
		if blk != leftParent && len(items) > 0 {
			child, trueParent = items[len(items)-1].ptr.Block, blk
		}
		blk = op.Next
	}
	if trueParent == 0 {
		t.Fatal("level 1 has a single page")
	}
	cases := []struct {
		name string
		path []storage.BlockNumber
	}{
		{"empty path", nil},
		{"stale hint left of the parent", []storage.BlockNumber{meta.Root, leftParent}},
		{"hint on the wrong level", []storage.BlockNumber{meta.Root}},
	}
	for _, c := range cases {
		got, _, err := bt.getStackBuf(c.path, child, 0)
		if err != nil || got != trueParent {
			t.Errorf("%s: getStackBuf = %d, %v; want %d", c.name, got, err, trueParent)
		}
	}
	if _, _, err := bt.getStackBuf(nil, 999999, 0); err == nil || !strings.Contains(err.Error(), "failed to re-find parent key") {
		t.Errorf("unknown child: err = %v, want failed to re-find parent key", err)
	}
}
