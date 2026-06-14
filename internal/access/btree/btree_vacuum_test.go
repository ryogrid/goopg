package btree

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// newVacuumTestPool creates a buffer pool for vacuum tests.
func newVacuumTestPool(t *testing.T) (*storage.Pool, storage.RelFileNode) {
	t.Helper()
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 512})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close(); _ = mgr.Close() })
	return pool, storage.RelFileNode{DBOid: 1, RelOid: 1, Fork: storage.MainFork}
}

// TestVacuumIndexPagesNoDeadTIDs verifies that VacuumIndexPages is a no-op
// when the dead-TID list is empty.
func TestVacuumIndexPagesNoDeadTIDs(t *testing.T) {
	pool, rel := newVacuumTestPool(t)
	entries := make([]BulkEntry, 100)
	for i := 0; i < 100; i++ {
		entries[i] = BulkEntry{Key: EncodeInt4(int32(i)), Ptr: storage.ItemPointer{Block: 0, Offset: uint16(i + 1)}}
	}
	tree, err := BulkCreate(pool, rel, entries)
	if err != nil {
		t.Fatalf("BulkCreate: %v", err)
	}

	removed, err := tree.VacuumIndexPages(nil)
	if err != nil {
		t.Fatalf("VacuumIndexPages: %v", err)
	}
	if removed != 0 {
		t.Errorf("expected 0 removed, got %d", removed)
	}

	// All entries must still be present.
	var count int
	_ = tree.RangeScan(nil, nil, func(_ []byte, _ storage.ItemPointer) (bool, error) {
		count++
		return true, nil
	})
	if count != 100 {
		t.Errorf("expected 100 entries, got %d", count)
	}
}

// TestVacuumIndexPagesPartial removes a subset of entries and verifies the
// remaining entries are still accessible and correct.
func TestVacuumIndexPagesPartial(t *testing.T) {
	pool, rel := newVacuumTestPool(t)
	const n = 200
	entries := make([]BulkEntry, n)
	for i := 0; i < n; i++ {
		entries[i] = BulkEntry{
			Key: EncodeInt4(int32(i)),
			Ptr: storage.ItemPointer{Block: storage.BlockNumber(i / 10), Offset: uint16(i%10 + 1)},
		}
	}
	tree, err := BulkCreate(pool, rel, entries)
	if err != nil {
		t.Fatalf("BulkCreate: %v", err)
	}

	// Mark every even-numbered entry as "dead".
	var deadTIDs []storage.ItemPointer
	for i := 0; i < n; i += 2 {
		deadTIDs = append(deadTIDs, storage.ItemPointer{
			Block:  storage.BlockNumber(i / 10),
			Offset: uint16(i%10 + 1),
		})
	}

	removed, err := tree.VacuumIndexPages(deadTIDs)
	if err != nil {
		t.Fatalf("VacuumIndexPages: %v", err)
	}
	if removed != n/2 {
		t.Errorf("expected %d removed, got %d", n/2, removed)
	}

	// Only odd-numbered entries should remain.
	var got []int32
	_ = tree.RangeScan(nil, nil, func(key []byte, _ storage.ItemPointer) (bool, error) {
		v, _ := DecodeInt4(key)
		got = append(got, v)
		return true, nil
	})
	if len(got) != n/2 {
		t.Errorf("expected %d remaining entries, got %d", n/2, len(got))
	}
	for _, v := range got {
		if v%2 == 0 {
			t.Errorf("even-numbered entry %d was not removed", v)
		}
	}
}

// TestVacuumIndexPagesAllDeleted is the DoD test for M0047-0002:
// after removing ALL index entries via VacuumIndexPages, the tree resets
// to a single empty root, and subsequent Inserts work correctly.
func TestVacuumIndexPagesAllDeleted(t *testing.T) {
	pool, rel := newVacuumTestPool(t)

	// Build a multi-page B-tree with n entries.
	const n = 500
	entries := make([]BulkEntry, n)
	for i := 0; i < n; i++ {
		entries[i] = BulkEntry{
			Key: EncodeInt4(int32(i)),
			Ptr: storage.ItemPointer{Block: storage.BlockNumber(i / 10), Offset: uint16(i%10 + 1)},
		}
	}
	tree, err := BulkCreate(pool, rel, entries)
	if err != nil {
		t.Fatalf("BulkCreate: %v", err)
	}

	// "Delete" all entries: mark all heap TIDs as dead.
	deadTIDs := make([]storage.ItemPointer, n)
	for i := 0; i < n; i++ {
		deadTIDs[i] = storage.ItemPointer{
			Block:  storage.BlockNumber(i / 10),
			Offset: uint16(i%10 + 1),
		}
	}

	removed, err := tree.VacuumIndexPages(deadTIDs)
	if err != nil {
		t.Fatalf("VacuumIndexPages all: %v", err)
	}
	if removed != n {
		t.Errorf("expected %d removed, got %d", n, removed)
	}

	// Tree must now be empty.
	var count int
	if err := tree.RangeScan(nil, nil, func(_ []byte, _ storage.ItemPointer) (bool, error) {
		count++
		return true, nil
	}); err != nil {
		t.Fatalf("RangeScan after vacuum: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 entries after full vacuum, got %d", count)
	}

	// Insert must work correctly on the reset tree.
	newKey := EncodeInt4(9999)
	if err := tree.Insert(newKey, storage.ItemPointer{Block: 0, Offset: 1}); err != nil {
		t.Fatalf("Insert after vacuum: %v", err)
	}
	var found bool
	_ = tree.RangeScan(newKey, newKey, func(_ []byte, _ storage.ItemPointer) (bool, error) {
		found = true
		return false, nil
	})
	if !found {
		t.Error("key inserted after vacuum not found")
	}
}

// TestVacuumIndexPagesSingleLeaf verifies deletion on a tiny tree that fits
// on a single leaf page (< 400 int4 entries).
func TestVacuumIndexPagesSingleLeaf(t *testing.T) {
	pool, rel := newVacuumTestPool(t)
	entries := []BulkEntry{
		{Key: EncodeInt4(1), Ptr: storage.ItemPointer{Block: 0, Offset: 1}},
		{Key: EncodeInt4(2), Ptr: storage.ItemPointer{Block: 0, Offset: 2}},
		{Key: EncodeInt4(3), Ptr: storage.ItemPointer{Block: 0, Offset: 3}},
	}
	tree, err := BulkCreate(pool, rel, entries)
	if err != nil {
		t.Fatalf("BulkCreate: %v", err)
	}

	// Delete key=2 only.
	removed, err := tree.VacuumIndexPages([]storage.ItemPointer{{Block: 0, Offset: 2}})
	if err != nil {
		t.Fatalf("VacuumIndexPages: %v", err)
	}
	if removed != 1 {
		t.Errorf("expected 1 removed, got %d", removed)
	}

	var got []int32
	_ = tree.RangeScan(nil, nil, func(key []byte, _ storage.ItemPointer) (bool, error) {
		v, _ := DecodeInt4(key)
		got = append(got, v)
		return true, nil
	})
	if len(got) != 2 || got[0] != 1 || got[1] != 3 {
		t.Errorf("expected [1,3], got %v", got)
	}
}

// TestVacuumIndexPagesLargeTree verifies correctness on a larger tree that
// spans multiple levels (n > ~400 entries for int4).
func TestVacuumIndexPagesLargeTree(t *testing.T) {
	pool, rel := newVacuumTestPool(t)
	const n = 2000
	entries := make([]BulkEntry, n)
	for i := 0; i < n; i++ {
		entries[i] = BulkEntry{
			Key: EncodeInt4(int32(i)),
			Ptr: storage.ItemPointer{Block: storage.BlockNumber(i / 100), Offset: uint16(i%100 + 1)},
		}
	}
	tree, err := BulkCreate(pool, rel, entries)
	if err != nil {
		t.Fatalf("BulkCreate: %v", err)
	}

	// Delete the first half.
	deadTIDs := make([]storage.ItemPointer, n/2)
	for i := 0; i < n/2; i++ {
		deadTIDs[i] = storage.ItemPointer{
			Block:  storage.BlockNumber(i / 100),
			Offset: uint16(i%100 + 1),
		}
	}

	removed, err := tree.VacuumIndexPages(deadTIDs)
	if err != nil {
		t.Fatalf("VacuumIndexPages: %v", err)
	}
	if removed != n/2 {
		t.Errorf("expected %d removed, got %d", n/2, removed)
	}

	// Remaining entries should be n/2..n-1.
	var count int
	_ = tree.RangeScan(nil, nil, func(key []byte, _ storage.ItemPointer) (bool, error) {
		v, _ := DecodeInt4(key)
		if v < int32(n/2) {
			t.Errorf("entry %d should have been vacuumed", v)
		}
		count++
		return true, nil
	})
	if count != n/2 {
		t.Errorf("expected %d remaining, got %d", n/2, count)
	}
}

// vacLeafChain walks the live leaf chain left-to-right from the leftmost leaf
// (deleted leaves are already unlinked from this chain) and returns the block
// numbers in order. It fails on a cycle.
func vacLeafChain(t *testing.T, bt *BTree) []storage.BlockNumber {
	t.Helper()
	lm, err := bt.findLeftmostLeaf()
	if err != nil {
		t.Fatalf("findLeftmostLeaf: %v", err)
	}
	var out []storage.BlockNumber
	seen := make(map[storage.BlockNumber]bool)
	for cur := lm; cur != storage.InvalidBlockNumber; {
		if seen[cur] {
			t.Fatalf("cycle in live leaf chain at block %d", cur)
		}
		seen[cur] = true
		slot, err := bt.pinR(cur)
		if err != nil {
			t.Fatalf("pinR(%d): %v", cur, err)
		}
		op := readOpaque(slot.Page())
		next := op.Next
		bt.unpinR(slot)
		out = append(out, cur)
		cur = next
	}
	return out
}

// vacLeafTIDs returns every heap TID stored on the given leaf block.
func vacLeafTIDs(t *testing.T, bt *BTree, blk storage.BlockNumber) []storage.ItemPointer {
	t.Helper()
	slot, err := bt.pinR(blk)
	if err != nil {
		t.Fatalf("pinR(%d): %v", blk, err)
	}
	items, err := pageItems(slot.Page())
	bt.unpinR(slot)
	if err != nil {
		t.Fatalf("pageItems(%d): %v", blk, err)
	}
	tids := make([]storage.ItemPointer, 0, len(items))
	for _, it := range items {
		tids = append(tids, it.ptr)
	}
	return tids
}

// vacOpaque reads a block's BTPageOpaque.
func vacOpaque(t *testing.T, bt *BTree, blk storage.BlockNumber) BTPageOpaque {
	t.Helper()
	slot, err := bt.pinR(blk)
	if err != nil {
		t.Fatalf("pinR(%d): %v", blk, err)
	}
	op := readOpaque(slot.Page())
	bt.unpinR(slot)
	return op
}

// TestVacuumIndexPagesAdjacentLeafRunRelinksLiveSiblings is the storage-layer
// regression for M0110-0010: deleting an ADJACENT run of empty leaves in one
// VacuumIndexPages pass must leave every surviving leaf's btpo_prev/btpo_next
// pointing at a LIVE (non-deleted) block, and the leaf sibling chain must be
// fully intact. Before the fix, unlinkEmptyLeaf relinked neighbours from the
// PHASE-1-captured pointers, so the survivors at a deleted run's edges were left
// pointing at a block deleted in the same pass.
func TestVacuumIndexPagesAdjacentLeafRunRelinksLiveSiblings(t *testing.T) {
	pool, rel := newVacuumTestPool(t)
	const n = 3000
	entries := make([]BulkEntry, n)
	for i := 0; i < n; i++ {
		entries[i] = BulkEntry{
			Key: EncodeInt4(int32(i)),
			Ptr: storage.ItemPointer{Block: storage.BlockNumber(i / 100), Offset: uint16(i%100 + 1)},
		}
	}
	tree, err := BulkCreate(pool, rel, entries)
	if err != nil {
		t.Fatalf("BulkCreate: %v", err)
	}

	leaves := vacLeafChain(t, tree)
	if len(leaves) < 5 {
		t.Fatalf("need >=5 leaves to delete an adjacent interior run, got %d", len(leaves))
	}
	// A contiguous run of interior leaves (each adjacent to the next target).
	targets := []storage.BlockNumber{leaves[1], leaves[2], leaves[3]}
	targetSet := map[storage.BlockNumber]bool{}
	var deadTIDs []storage.ItemPointer
	for _, blk := range targets {
		tids := vacLeafTIDs(t, tree, blk)
		if len(tids) == 0 {
			t.Fatalf("target interior leaf %d had no entries", blk)
		}
		deadTIDs = append(deadTIDs, tids...)
		targetSet[blk] = true
	}

	removed, err := tree.VacuumIndexPages(deadTIDs)
	if err != nil {
		t.Fatalf("VacuumIndexPages: %v", err)
	}
	if removed != len(deadTIDs) {
		t.Fatalf("VacuumIndexPages removed %d, expected %d", removed, len(deadTIDs))
	}

	// Every target leaf must now be flagged deleted (the unlink really ran).
	for _, blk := range targets {
		if !vacOpaque(t, tree, blk).IsDeleted() {
			t.Fatalf("target leaf %d was expected deleted but is still live", blk)
		}
	}

	// Walk the live leaf chain and assert structural integrity: no survivor's
	// prev/next references a deleted block, and the chain is bidirectionally
	// consistent (a.next==b implies b.prev==a) with proper Invalid edges.
	live := vacLeafChain(t, tree)
	for _, blk := range live {
		if targetSet[blk] {
			t.Fatalf("deleted leaf %d is still reachable on the live chain", blk)
		}
	}
	for i, blk := range live {
		op := vacOpaque(t, tree, blk)
		if op.Prev != storage.InvalidBlockNumber {
			if vacOpaque(t, tree, op.Prev).IsDeleted() {
				t.Fatalf("survivor leaf %d btpo_prev=%d points at a DELETED block (M0110-0010 regression)", blk, op.Prev)
			}
		}
		if op.Next != storage.InvalidBlockNumber {
			if vacOpaque(t, tree, op.Next).IsDeleted() {
				t.Fatalf("survivor leaf %d btpo_next=%d points at a DELETED block (M0110-0010 regression)", blk, op.Next)
			}
		}
		// Edge invariants.
		if i == 0 && op.Prev != storage.InvalidBlockNumber {
			t.Fatalf("leftmost live leaf %d has non-Invalid btpo_prev=%d", blk, op.Prev)
		}
		if i == len(live)-1 && op.Next != storage.InvalidBlockNumber {
			t.Fatalf("rightmost live leaf %d has non-Invalid btpo_next=%d", blk, op.Next)
		}
		// Bidirectional consistency with the next live leaf.
		if i < len(live)-1 {
			if op.Next != live[i+1] {
				t.Fatalf("leaf %d btpo_next=%d but next live leaf is %d", blk, op.Next, live[i+1])
			}
			if rp := vacOpaque(t, tree, live[i+1]).Prev; rp != blk {
				t.Fatalf("leaf %d btpo_prev=%d but its left live neighbour is %d", live[i+1], rp, blk)
			}
		}
	}

	// Surviving entry count must be exact, and a full scan must not error.
	want := n - len(deadTIDs)
	var count int
	if err := tree.RangeScan(nil, nil, func(_ []byte, _ storage.ItemPointer) (bool, error) {
		count++
		return true, nil
	}); err != nil {
		t.Fatalf("RangeScan after adjacent-run vacuum: %v", err)
	}
	if count != want {
		t.Fatalf("expected %d surviving entries, got %d", want, count)
	}
}
