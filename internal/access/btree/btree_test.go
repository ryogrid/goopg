package btree

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

func newTestTree(t *testing.T) (*BTree, *storage.Pool, func()) {
	t.Helper()
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 32})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	rel := storage.RelFileNode{DBOid: 1, RelOid: 9000, Fork: storage.MainFork}
	bt, err := Create(pool, rel)
	if err != nil {
		t.Fatalf("btree.Create: %v", err)
	}
	cleanup := func() {
		_ = pool.Close()
		_ = mgr.Close()
	}
	return bt, pool, cleanup
}

// TestInsertSearchRoundTrip pins the basic contract: every key we
// insert is searchable, missing keys return found=false.
func TestInsertSearchRoundTrip(t *testing.T) {
	bt, _, cleanup := newTestTree(t)
	defer cleanup()

	keys := []int32{42, 7, 99, 1, 1000, -5}
	for i, k := range keys {
		ptr := storage.ItemPointer{Block: storage.BlockNumber(100 + i), Offset: uint16(i + 1)}
		if err := bt.Insert(EncodeInt4(k), ptr); err != nil {
			t.Fatalf("Insert(%d): %v", k, err)
		}
	}

	for i, k := range keys {
		ptr, ok, err := bt.Search(EncodeInt4(k))
		if err != nil {
			t.Fatalf("Search(%d): %v", k, err)
		}
		if !ok {
			t.Fatalf("Search(%d): not found", k)
		}
		want := storage.ItemPointer{Block: storage.BlockNumber(100 + i), Offset: uint16(i + 1)}
		if ptr != want {
			t.Errorf("Search(%d) = %+v, want %+v", k, ptr, want)
		}
	}

	if _, ok, err := bt.Search(EncodeInt4(8888)); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Error("Search(8888) should not be found")
	}
}

// TestLeafSplit pushes enough rows to overflow a single leaf and verifies
// every key is still searchable after the resulting splits.
func TestLeafSplit(t *testing.T) {
	bt, _, cleanup := newTestTree(t)
	defer cleanup()

	const N = 800 // big enough to force several splits
	for i := 0; i < N; i++ {
		ptr := storage.ItemPointer{Block: storage.BlockNumber(i + 1), Offset: 1}
		if err := bt.Insert(EncodeInt4(int32(i)), ptr); err != nil {
			t.Fatalf("Insert(%d): %v", i, err)
		}
	}

	for i := 0; i < N; i++ {
		ptr, ok, err := bt.Search(EncodeInt4(int32(i)))
		if err != nil {
			t.Fatalf("Search(%d): %v", i, err)
		}
		if !ok {
			t.Fatalf("Search(%d): not found after splits", i)
		}
		want := storage.ItemPointer{Block: storage.BlockNumber(i + 1), Offset: 1}
		if ptr != want {
			t.Errorf("Search(%d) = %+v, want %+v", i, ptr, want)
		}
	}
}

// TestRangeScan verifies left-to-right leaf walking emits keys in order
// and respects the [lo, hi] bounds.
func TestRangeScan(t *testing.T) {
	bt, _, cleanup := newTestTree(t)
	defer cleanup()

	for _, k := range []int32{10, 20, 5, 15, 25, 30, 1, 50} {
		if err := bt.Insert(EncodeInt4(k), storage.ItemPointer{Block: storage.BlockNumber(k), Offset: 1}); err != nil {
			t.Fatal(err)
		}
	}

	var got []int32
	err := bt.RangeScan(EncodeInt4(10), EncodeInt4(25), func(key []byte, _ storage.ItemPointer) (bool, error) {
		k, _ := DecodeInt4(key)
		got = append(got, k)
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []int32{10, 15, 20, 25}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}

// TestConcurrentSearchAfterInserts pins Landing 1 of milestone 0002:
// after a tree is populated, many goroutines can call Search in
// parallel without serialising through the global lock. The test
// doesn't measure throughput — go test -race is the real
// signal — but it does verify correctness under concurrency.
func TestConcurrentSearchAfterInserts(t *testing.T) {
	bt, _, cleanup := newTestTree(t)
	defer cleanup()

	const N = 200
	for i := 0; i < N; i++ {
		ptr := storage.ItemPointer{Block: storage.BlockNumber(i + 1), Offset: 1}
		if err := bt.Insert(EncodeInt4(int32(i)), ptr); err != nil {
			t.Fatalf("Insert(%d): %v", i, err)
		}
	}

	const goroutines = 8
	const lookupsPer = 500
	var found atomic.Uint64
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < lookupsPer; i++ {
				key := int32(i % N)
				_, ok, err := bt.Search(EncodeInt4(key))
				if err != nil {
					t.Errorf("Search(%d): %v", key, err)
					return
				}
				if ok {
					found.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	if found.Load() != goroutines*lookupsPer {
		t.Errorf("found %d/%d concurrent search hits", found.Load(), goroutines*lookupsPer)
	}
}

// TestReopen ensures a freshly-Open'd handle observes the same metapage
// state — i.e. the on-disk format is internally consistent.
func TestReopen(t *testing.T) {
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	defer mgr.Close()
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 8})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	rel := storage.RelFileNode{DBOid: 1, RelOid: 9001, Fork: storage.MainFork}

	first, err := Create(pool, rel)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Insert(EncodeInt4(123), storage.ItemPointer{Block: 7, Offset: 1}); err != nil {
		t.Fatal(err)
	}
	if err := pool.FlushAll(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(pool, rel)
	if err != nil {
		t.Fatal(err)
	}
	ptr, ok, err := reopened.Search(EncodeInt4(123))
	if err != nil || !ok {
		t.Fatalf("Search after reopen: ok=%v err=%v", ok, err)
	}
	if ptr.Block != 7 {
		t.Errorf("Search ptr = %+v, want Block=7", ptr)
	}
}
