package nbtree

// review/260831-2 NB-4 / NB-5 — the rightmost-leaf insert cache.
//
// NB-4: the descent stored the cache only when `op.Next == 0`, but readOpaque
// translates the on-disk P_NONE sibling into storage.InvalidBlockNumber, so
// the condition was never true on a rightmost page and the cache stayed empty
// for the life of the tree; tryInsertOnCachedRightmost's mirror-image test
// (`op.Next != 0`) would have declared every entry stale anyway. The whole
// fast path was dead.
//
// NB-5: with the cache live, the entry point has to refuse a page that is
// deleted / half-dead / mid-split — the descent skips such pages, and an
// insert onto one that unlinkEmptyLeaf then recycles would land on an
// unrelated key range.

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

func TestRightmostLeafCacheIsPopulatedAndUsed(t *testing.T) {
	bt, _, cleanup := newTestTree(t)
	defer cleanup()

	// Append-shaped inserts, enough to split past a single-page tree.
	const n = 400
	for i := range n {
		ptr := storage.ItemPointer{Block: storage.BlockNumber(i), Offset: uint16(i%100 + 1)}
		if err := bt.Insert(EncodeInt4(int32(i)), ptr); err != nil {
			t.Fatalf("Insert(%d): %v", i, err)
		}
	}

	cached := bt.rightmostLeafBlk.Load()
	if cached == 0 {
		t.Fatal("rightmost-leaf cache is empty after 400 ascending inserts — the fast path is dead")
	}

	before := itemsOnBlock(t, bt, storage.BlockNumber(cached))
	if err := bt.Insert(EncodeInt4(n), storage.ItemPointer{Block: 1, Offset: 1}); err != nil {
		t.Fatalf("Insert(%d): %v", n, err)
	}
	if after := itemsOnBlock(t, bt, storage.BlockNumber(cached)); after != before+1 {
		t.Errorf("cached leaf %d held %d items, %d after the next ascending insert — want %d (the insert did not use the cache)",
			cached, before, after, before+1)
	}

	for i := range n + 1 {
		if _, ok, err := bt.Search(EncodeInt4(int32(i))); err != nil || !ok {
			t.Fatalf("Search(%d) = (ok=%v, err=%v)", i, ok, err)
		}
	}
}

func TestRightmostLeafCacheRefusesUnlinkablePage(t *testing.T) {
	for _, tc := range []struct {
		name string
		flag uint16
	}{
		{name: "deleted", flag: BTDeleted},
		{name: "half-dead", flag: BTHalfDead},
		{name: "incomplete-split", flag: BTIncompleteSplit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bt, _, cleanup := newTestTree(t)
			defer cleanup()

			for i := range 200 {
				ptr := storage.ItemPointer{Block: storage.BlockNumber(i), Offset: uint16(i%100 + 1)}
				if err := bt.Insert(EncodeInt4(int32(i)), ptr); err != nil {
					t.Fatalf("Insert(%d): %v", i, err)
				}
			}
			blk := storage.BlockNumber(bt.rightmostLeafBlk.Load())
			if blk == 0 {
				t.Fatal("cache empty")
			}

			slot, err := bt.pinW(blk)
			if err != nil {
				t.Fatalf("pinW(%d): %v", blk, err)
			}
			op := readOpaque(slot.Page())
			op.Flags |= tc.flag
			writeOpaque(slot.Page(), op)
			bt.unpinW(slot)

			before := itemsOnBlock(t, bt, blk)
			it := item{ptr: storage.ItemPointer{Block: 7, Offset: 7}, key: EncodeInt4(9999)}
			ok, err := bt.tryInsertOnCachedRightmost(blk, it)
			if err != nil {
				t.Fatalf("tryInsertOnCachedRightmost: %v", err)
			}
			if ok {
				t.Errorf("inserted onto a %s page; want a cache miss", tc.name)
			}
			if after := itemsOnBlock(t, bt, blk); after != before {
				t.Errorf("%s page item count %d -> %d", tc.name, before, after)
			}
			if got := bt.rightmostLeafBlk.Load(); got != 0 {
				t.Errorf("cache still holds %d after a miss; want cleared", got)
			}
		})
	}
}

func itemsOnBlock(t *testing.T, bt *BTree, blk storage.BlockNumber) int {
	t.Helper()
	slot, err := bt.pinR(blk)
	if err != nil {
		t.Fatalf("pinR(%d): %v", blk, err)
	}
	defer bt.unpinR(slot)
	n, err := PGDataItemCount(slot.Page())
	if err != nil {
		t.Fatalf("PGDataItemCount(%d): %v", blk, err)
	}
	return n
}
