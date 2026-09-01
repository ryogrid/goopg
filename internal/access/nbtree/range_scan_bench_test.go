package nbtree

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// BenchmarkRangeScanPointLookup pins the M0091-0002 acceptance:
// a point lookup via RangeScan(key, key, fn) must NOT allocate on
// the non-posting branch (which is the pgbench pkey case). Pre-fix
// each lookup allocated ~400 byte-slices per visited leaf page;
// post-fix the parsed `key` aliases the still-pinned page.
//
// Set up: insert 10 000 int32 keys; benchmark loops point-lookups
// over a sample of those keys.
func BenchmarkRangeScanPointLookup(b *testing.B) {
	bt, _, cleanup := newTestTreeForBench(b)
	defer cleanup()

	const N = 10_000
	for i := 0; i < N; i++ {
		ptr := storage.ItemPointer{Block: storage.BlockNumber(i + 1), Offset: 1}
		if err := bt.Insert(EncodeInt4(int32(i)), ptr); err != nil {
			b.Fatalf("Insert(%d): %v", i, err)
		}
	}

	var got int
	cb := func(key []byte, ptr storage.ItemPointer) (bool, error) {
		got++
		_ = key
		return true, nil
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		k := EncodeInt4(int32(i % N))
		if err := bt.RangeScan(k, k, cb); err != nil {
			b.Fatalf("RangeScan: %v", err)
		}
	}
}

func newTestTreeForBench(b *testing.B) (*BTree, *storage.Pool, func()) {
	b.Helper()
	dir := b.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 64})
	if err != nil {
		b.Fatalf("NewPool: %v", err)
	}
	rel := storage.RelFileNode{DBOid: 1, RelOid: 9100, Fork: storage.MainFork}
	bt, err := Create(pool, rel)
	if err != nil {
		b.Fatalf("nbtree.Create: %v", err)
	}
	cleanup := func() {
		_ = pool.Close()
		_ = mgr.Close()
	}
	return bt, pool, cleanup
}

// BenchmarkSearchDescent measures a point Search over a tree deep enough to
// have internal pages (review/260831 NB-9): every probe of the internal-page
// binary search used to copy the probed key out of the page and throw it away.
func BenchmarkSearchDescent(b *testing.B) {
	bt, _, cleanup := newTestTreeForBench(b)
	defer cleanup()

	const N = 200_000
	for i := 0; i < N; i++ {
		ptr := storage.ItemPointer{Block: storage.BlockNumber(i + 1), Offset: 1}
		if err := bt.Insert(EncodeInt4(int32(i)), ptr); err != nil {
			b.Fatalf("Insert(%d): %v", i, err)
		}
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, ok, err := bt.Search(EncodeInt4(int32((i * 7919) % N))); err != nil || !ok {
			b.Fatalf("Search: ok=%v err=%v", ok, err)
		}
	}
}

// BenchmarkInsertSorted measures the in-page insertion binary search
// (review/260831 NB-9), which probed with a copying parse as well.
func BenchmarkInsertSorted(b *testing.B) {
	bt, _, cleanup := newTestTreeForBench(b)
	defer cleanup()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		ptr := storage.ItemPointer{Block: storage.BlockNumber(i + 1), Offset: 1}
		if err := bt.Insert(EncodeInt4(int32(i)), ptr); err != nil {
			b.Fatalf("Insert(%d): %v", i, err)
		}
	}
}
