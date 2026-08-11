package btree

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// Whole-tree guards for M0130-S11.4 slice 3a. The per-tuple tests in
// pgitem_test.go pin the encoder; these walk a tree the ENGINE built and assert
// upstream's structural rule on every page:
//
//   - every item on an internal page is a pivot tuple (nbtree.h: internal-page
//     items are downlinks, never heap pointers), whose t_tid block half is the
//     child and whose leftmost instance carries zero key attributes;
//   - every P_HIKEY separator is a pivot tuple, on leaf pages too (_bt_truncate
//     produces the leaf high key as well as the internal one);
//   - every leaf DATA item is NOT a pivot — a plain tuple (or a posting list).
//
// This is the tier that catches a writer that was missed rather than an encoder
// that is wrong: splits, the bulk loader, the VACUUM page rewrites and the WAL
// replay path each rebuild pages independently, and the pivot bit is exactly
// the kind of in-memory flag a `item{...}` literal silently drops.

// walkPivotInvariants applies the rules above to every block of the tree.
func walkPivotInvariants(t *testing.T, pool *storage.Pool, rel storage.RelFileNode, nblocks storage.BlockNumber) (internalPages, leafPagesWithHighKey int) {
	t.Helper()
	for blk := rootStart; blk < nblocks; blk++ {
		slot, err := pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			t.Fatalf("pin block %d: %v", blk, err)
		}
		slot.RLock()
		p := make(storage.Page, storage.BlockSize)
		copy(p, slot.Page())
		slot.RUnlock()
		pool.Unpin(slot)

		if err := CheckPGBTPage(p, blk); err != nil {
			t.Fatalf("block %d fails _bt_checkpage: %v", blk, err)
		}
		op := ParseOpaque(p)
		if op.Flags&BTDeleted != 0 {
			continue
		}

		if raw, ok, err := PGHighKeyRaw(p); err != nil {
			t.Fatalf("block %d: PGHighKeyRaw: %v", blk, err)
		} else if ok {
			if !BTreeTupleIsPivot(raw) {
				t.Errorf("block %d: P_HIKEY is not a pivot tuple (t_info=%#x)", blk, pgTInfo(raw))
			}
			if op.IsLeaf() {
				leafPagesWithHighKey++
			}
		}

		n, err := PGDataItemCount(p)
		if err != nil {
			t.Fatalf("block %d: PGDataItemCount: %v", blk, err)
		}
		for i := 0; i < n; i++ {
			raw, err := pgGetItemRawAllowDead(p, uint16(i+1))
			if err != nil {
				t.Fatalf("block %d item %d: %v", blk, i, err)
			}
			switch {
			case op.IsLeaf():
				if BTreeTupleIsPivot(raw) {
					t.Errorf("block %d (leaf) data item %d is a pivot tuple", blk, i)
				}
			default:
				if !BTreeTupleIsPivot(raw) {
					t.Errorf("block %d (internal) item %d is not a pivot tuple (t_info=%#x)", blk, i, pgTInfo(raw))
				}
				wantNAtts := uint16(1)
				if i == 0 {
					wantNAtts = 0 // minus infinity
				}
				if got := BTreeTupleGetNAtts(raw, 1); got != wantNAtts {
					t.Errorf("block %d (internal) item %d natts = %d, want %d", blk, i, got, wantNAtts)
				}
				if child := BTreeTupleGetDownLink(raw); child == 0 || child >= nblocks {
					t.Errorf("block %d (internal) item %d downlink = %d, out of range [1,%d)", blk, i, child, nblocks)
				}
			}
		}
		if !op.IsLeaf() {
			internalPages++
		}
	}
	return internalPages, leafPagesWithHighKey
}

// TestInsertSplitProducesPivotTuples drives the incremental writer (Insert →
// split → new root) far enough to build at least one internal level.
func TestInsertSplitProducesPivotTuples(t *testing.T) {
	bt, pool, cleanup := newTestTree(t)
	defer cleanup()

	const n = 3000
	for i := 0; i < n; i++ {
		ptr := storage.ItemPointer{Block: storage.BlockNumber(i/100 + 1), Offset: uint16(i%100 + 1)}
		if err := bt.Insert(EncodeInt4(int32(i)), ptr); err != nil {
			t.Fatalf("Insert(%d): %v", i, err)
		}
	}

	nblocks, err := pool.NBlocks(bt.rel)
	if err != nil {
		t.Fatalf("NBlocks: %v", err)
	}
	internal, leafHK := walkPivotInvariants(t, pool, bt.rel, nblocks)
	if internal == 0 {
		t.Fatalf("tree of %d keys has no internal page (%d blocks) — the guard checked nothing", n, nblocks)
	}
	if leafHK == 0 {
		t.Fatal("no non-rightmost leaf page — the leaf high-key rule was not exercised")
	}

	// The tree must still be searchable through its own pivots.
	for _, k := range []int32{0, 1, n / 2, n - 1} {
		if _, ok, err := bt.Search(EncodeInt4(k)); err != nil || !ok {
			t.Errorf("Search(%d) = ok %v, err %v after the pivot flip", k, ok, err)
		}
	}
}

// TestBulkCreateProducesPivotTuples covers the second, independent page writer:
// the bulk loader builds whole levels at once rather than splitting upward.
func TestBulkCreateProducesPivotTuples(t *testing.T) {
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 32})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer func() {
		_ = pool.Close()
		_ = mgr.Close()
	}()

	entries := make([]BulkEntry, 4000)
	for i := range entries {
		entries[i] = BulkEntry{
			Key: EncodeInt4(int32(i)),
			Ptr: storage.ItemPointer{Block: storage.BlockNumber(i/100 + 1), Offset: uint16(i%100 + 1)},
		}
	}
	rel := storage.RelFileNode{DBOid: 1, RelOid: 9100, Fork: storage.MainFork}
	bt, err := BulkCreate(pool, rel, entries)
	if err != nil {
		t.Fatalf("BulkCreate: %v", err)
	}

	nblocks, err := pool.NBlocks(rel)
	if err != nil {
		t.Fatalf("NBlocks: %v", err)
	}
	internal, leafHK := walkPivotInvariants(t, pool, rel, nblocks)
	if internal == 0 {
		t.Fatal("bulk-loaded tree has no internal page — the guard checked nothing")
	}
	if leafHK == 0 {
		t.Fatal("bulk-loaded tree has no non-rightmost leaf page")
	}
	if _, ok, err := bt.Search(EncodeInt4(1234)); err != nil || !ok {
		t.Errorf("Search(1234) = ok %v, err %v", ok, err)
	}
}
