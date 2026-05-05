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
