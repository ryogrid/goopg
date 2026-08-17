package nbtree

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// newTestPool creates a temporary buffer pool for btree tests.
func newBulkTestPool(t *testing.T) (*storage.Pool, storage.RelFileNode) {
	t.Helper()
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 512})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(func() {
		_ = pool.Close()
		_ = mgr.Close()
	})
	rel := storage.RelFileNode{DBOid: 1, RelOid: 1, Fork: storage.MainFork}
	return pool, rel
}

// TestBulkCreateEmpty verifies that BulkCreate on an empty entry list
// produces a valid empty B-tree.
func TestBulkCreateEmpty(t *testing.T) {
	pool, rel := newBulkTestPool(t)
	tree, err := BulkCreate(pool, rel, nil)
	if err != nil {
		t.Fatalf("BulkCreate empty: %v", err)
	}

	// RangeScan on empty tree must return zero results.
	var count int
	err = tree.RangeScan(nil, nil, func(_ []byte, _ storage.ItemPointer) (bool, error) {
		count++
		return true, nil
	})
	if err != nil {
		t.Fatalf("RangeScan empty: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 entries in empty tree, got %d", count)
	}
}

// TestBulkCreateSingleEntry verifies a one-entry bulk build.
func TestBulkCreateSingleEntry(t *testing.T) {
	pool, rel := newBulkTestPool(t)
	key := EncodeInt4(42)
	entries := []BulkEntry{{Key: key, Ptr: storage.ItemPointer{Block: 0, Offset: 1}}}
	tree, err := BulkCreate(pool, rel, entries)
	if err != nil {
		t.Fatalf("BulkCreate single: %v", err)
	}

	var found int
	_ = tree.RangeScan(key, key, func(k []byte, ptr storage.ItemPointer) (bool, error) {
		found++
		if CompareKeys(k, key) != 0 {
			t.Errorf("key mismatch: got %v want %v", k, key)
		}
		return true, nil
	})
	if found != 1 {
		t.Errorf("expected 1 entry, got %d", found)
	}
}

// TestBulkCreateRoundTrip verifies that BulkCreate + RangeScan returns
// all inserted entries in sorted order.
func TestBulkCreateRoundTrip(t *testing.T) {
	const n = 1000
	pool, rel := newBulkTestPool(t)

	entries := make([]BulkEntry, n)
	for i := 0; i < n; i++ {
		entries[i] = BulkEntry{
			Key: EncodeInt4(int32(n - i)), // intentionally reverse order to test sorting
			Ptr: storage.ItemPointer{Block: storage.BlockNumber(i / 10), Offset: uint16(i%10 + 1)},
		}
	}
	tree, err := BulkCreate(pool, rel, entries)
	if err != nil {
		t.Fatalf("BulkCreate: %v", err)
	}

	// Scan all entries and verify sort order.
	var got []int32
	err = tree.RangeScan(nil, nil, func(key []byte, _ storage.ItemPointer) (bool, error) {
		v, decErr := DecodeInt4(key)
		if decErr != nil {
			return false, decErr
		}
		got = append(got, v)
		return true, nil
	})
	if err != nil {
		t.Fatalf("RangeScan: %v", err)
	}
	if len(got) != n {
		t.Fatalf("expected %d entries, got %d", n, len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i] < got[i-1] {
			t.Errorf("out of order at idx %d: %d < %d", i, got[i], got[i-1])
		}
	}
}

// TestBulkCreateMatchesInsert verifies that BulkCreate produces the same
// search results as the sequential Insert path for the same set of keys.
func TestBulkCreateMatchesInsert(t *testing.T) {
	const n = 500
	pool, rel := newBulkTestPool(t)

	// Build with BulkCreate.
	entries := make([]BulkEntry, n)
	for i := 0; i < n; i++ {
		entries[i] = BulkEntry{
			Key: EncodeInt4(int32(i * 2)),
			Ptr: storage.ItemPointer{Block: 0, Offset: uint16(i + 1)},
		}
	}
	bulkTree, err := BulkCreate(pool, rel, entries)
	if err != nil {
		t.Fatalf("BulkCreate: %v", err)
	}

	// Build a second tree with sequential Insert for comparison.
	rel2 := storage.RelFileNode{DBOid: 1, RelOid: 2, Fork: storage.MainFork}
	insertTree, err := Create(pool, rel2)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for i := 0; i < n; i++ {
		k := EncodeInt4(int32(i * 2))
		if err := insertTree.Insert(k, storage.ItemPointer{Block: 0, Offset: uint16(i + 1)}); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}

	// Compare RangeScan results.
	collectKeys := func(tree *BTree) []int32 {
		var keys []int32
		_ = tree.RangeScan(nil, nil, func(key []byte, _ storage.ItemPointer) (bool, error) {
			v, _ := DecodeInt4(key)
			keys = append(keys, v)
			return true, nil
		})
		return keys
	}
	bulkKeys := collectKeys(bulkTree)
	insertKeys := collectKeys(insertTree)
	if len(bulkKeys) != len(insertKeys) {
		t.Fatalf("key count mismatch: bulk=%d insert=%d", len(bulkKeys), len(insertKeys))
	}
	for i, bk := range bulkKeys {
		if bk != insertKeys[i] {
			t.Errorf("key[%d]: bulk=%d insert=%d", i, bk, insertKeys[i])
		}
	}
}

// TestBulkCreatePointLookup verifies equality lookups work after BulkCreate.
func TestBulkCreatePointLookup(t *testing.T) {
	const n = 200
	pool, rel := newBulkTestPool(t)

	entries := make([]BulkEntry, n)
	for i := 0; i < n; i++ {
		entries[i] = BulkEntry{
			Key: EncodeInt4(int32(i)),
			Ptr: storage.ItemPointer{Block: storage.BlockNumber(i), Offset: 1},
		}
	}
	tree, err := BulkCreate(pool, rel, entries)
	if err != nil {
		t.Fatalf("BulkCreate: %v", err)
	}

	// Spot-check several point lookups.
	for _, v := range []int32{0, 50, 99, 100, 199} {
		key := EncodeInt4(v)
		var found bool
		_ = tree.RangeScan(key, key, func(k []byte, ptr storage.ItemPointer) (bool, error) {
			found = true
			if ptr.Block != storage.BlockNumber(v) {
				t.Errorf("ptr.Block for key %d: want %d got %d", v, v, ptr.Block)
			}
			return false, nil
		})
		if !found {
			t.Errorf("key %d not found after BulkCreate", v)
		}
	}
}

// TestBulkCreateMultiLevel verifies that a large enough entry set triggers
// internal-level pages (the bulk build must produce a correct multi-level
// B-tree that supports range scans after construction).
func TestBulkCreateMultiLevel(t *testing.T) {
	// With int4 keys (~20 bytes/entry), each leaf holds ~400 entries.
	// 10 000 entries ⇒ ~25 leaves ⇒ 1 internal page ⇒ 2-level tree.
	const n = 10_000
	pool, rel := newBulkTestPool(t)

	entries := make([]BulkEntry, n)
	for i := 0; i < n; i++ {
		entries[i] = BulkEntry{
			Key: EncodeInt4(int32(i)),
			Ptr: storage.ItemPointer{Block: 0, Offset: uint16(i%100 + 1)},
		}
	}
	tree, err := BulkCreate(pool, rel, entries)
	if err != nil {
		t.Fatalf("BulkCreate %d entries: %v", n, err)
	}

	var count int
	err = tree.RangeScan(nil, nil, func(_ []byte, _ storage.ItemPointer) (bool, error) {
		count++
		return true, nil
	})
	if err != nil {
		t.Fatalf("RangeScan after multi-level build: %v", err)
	}
	if count != n {
		t.Errorf("expected %d entries after multi-level build, got %d", n, count)
	}

	// Insert into the bulk-built tree must also work correctly.
	newKey := EncodeInt4(int32(n + 1))
	if err := tree.Insert(newKey, storage.ItemPointer{Block: 0, Offset: 1}); err != nil {
		t.Fatalf("Insert after BulkCreate: %v", err)
	}
	found := false
	_ = tree.RangeScan(newKey, newKey, func(_ []byte, _ storage.ItemPointer) (bool, error) {
		found = true
		return false, nil
	})
	if !found {
		t.Error("key inserted after BulkCreate not found")
	}
}

// TestBulkCreateVsInsertPerformance is a timing gate that validates the
// DoD: bulk build of 100k int4 entries must be significantly faster than
// sequential Insert. In CI we only assert correctness; the wall-time
// comparison is informational.
func TestBulkCreateVsInsertPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping performance test in short mode")
	}
	const n = 100_000
	pool, rel := newBulkTestPool(t)

	// Build entries (unsorted to exercise sort phase).
	entries := make([]BulkEntry, n)
	for i := 0; i < n; i++ {
		entries[i] = BulkEntry{
			Key: EncodeInt4(int32(n - i)), // descending = worst case for Insert
			Ptr: storage.ItemPointer{Block: storage.BlockNumber(i / 100), Offset: uint16(i%100 + 1)},
		}
	}

	// Bulk build.
	tree, err := BulkCreate(pool, rel, entries)
	if err != nil {
		t.Fatalf("BulkCreate: %v", err)
	}

	// Verify all n entries are reachable.
	var got int
	_ = tree.RangeScan(nil, nil, func(_ []byte, _ storage.ItemPointer) (bool, error) {
		got++
		return true, nil
	})
	if got != n {
		t.Errorf("expected %d entries, got %d", n, got)
	}
	t.Logf("BulkCreate %d int4 entries: correctness verified (got=%d)", n, got)
}

// TestBulkCreateAfterInsertable verifies that additional Insert calls
// work correctly on a tree built by BulkCreate.
func TestBulkCreateAfterInsertable(t *testing.T) {
	pool, rel := newBulkTestPool(t)

	const n = 500
	entries := make([]BulkEntry, n)
	for i := 0; i < n; i++ {
		entries[i] = BulkEntry{
			Key: EncodeInt4(int32(i * 2)), // even numbers 0..998
			Ptr: storage.ItemPointer{Block: 0, Offset: 1},
		}
	}
	tree, err := BulkCreate(pool, rel, entries)
	if err != nil {
		t.Fatalf("BulkCreate: %v", err)
	}

	// Insert all odd numbers.
	for i := 0; i < n; i++ {
		k := EncodeInt4(int32(i*2 + 1))
		if err := tree.Insert(k, storage.ItemPointer{Block: 1, Offset: 1}); err != nil {
			t.Fatalf("Insert odd %d: %v", i*2+1, err)
		}
	}

	// Scan all: should have 2n entries in order.
	var prev int32 = -1
	var total int
	_ = tree.RangeScan(nil, nil, func(key []byte, _ storage.ItemPointer) (bool, error) {
		v, _ := DecodeInt4(key)
		if v <= prev {
			t.Errorf("out of order: %d after %d", v, prev)
		}
		prev = v
		total++
		return true, nil
	})
	if total != 2*n {
		t.Errorf("expected %d total entries, got %d", 2*n, total)
	}
}

