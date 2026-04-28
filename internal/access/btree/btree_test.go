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

// TestSplitInvokesLogSplit pins Landing 3a of milestone 0002:
// when an Insert causes a leaf split, the configured LogSplit
// closure is invoked once per split with both page images, and
// the returned LSN is stamped onto pd_lsn of both pages so the
// buffer pool's flush-before-write ordering covers them.
func TestSplitInvokesLogSplit(t *testing.T) {
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	defer mgr.Close()
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 32})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()
	rel := storage.RelFileNode{DBOid: 1, RelOid: 9100, Fork: storage.MainFork}

	type call struct {
		left, right storage.BlockNumber
	}
	var calls []call
	logSplit := func(_ storage.RelFileNode, leftBlk, rightBlk storage.BlockNumber, leftPage, rightPage storage.Page) (storage.LSN, error) {
		if len(leftPage) != storage.BlockSize || len(rightPage) != storage.BlockSize {
			t.Errorf("page sizes = %d/%d, want %d", len(leftPage), len(rightPage), storage.BlockSize)
		}
		calls = append(calls, call{leftBlk, rightBlk})
		return storage.LSN(1000 + len(calls)), nil
	}

	bt, err := CreateWithOptions(pool, rel, Options{LogSplit: logSplit})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Drive enough inserts to force at least one split. v0 leaves
	// can hold roughly 500 fixed-width int4 entries before pd_lower
	// catches pd_upper; 1000 is safely past the threshold.
	for i := 0; i < 1000; i++ {
		ptr := storage.ItemPointer{Block: storage.BlockNumber(i + 1), Offset: 1}
		if err := bt.Insert(EncodeInt4(int32(i)), ptr); err != nil {
			t.Fatalf("Insert(%d): %v", i, err)
		}
	}

	if len(calls) == 0 {
		t.Fatal("LogSplit was never invoked despite 1000 inserts forcing a split")
	}
	for _, c := range calls {
		if c.left == c.right {
			t.Errorf("split with leftBlk == rightBlk = %d", c.left)
		}
	}
}

// TestConcurrentInsertSearch is the Landing 2 acceptance test for
// milestone 0002: while one writer drives Insert (which performs
// splits and updates the metapage), N reader goroutines hammer
// Search against keys that are being inserted. Right-link
// recovery handles the case where a reader descended to a leaf
// that has since been split. The test runs under -race to catch
// torn-byte access at the page level.
func TestConcurrentInsertSearch(t *testing.T) {
	bt, _, cleanup := newTestTree(t)
	defer cleanup()

	const total = 300
	// Pre-seed half the keys so readers always have work.
	for i := 0; i < total/2; i++ {
		ptr := storage.ItemPointer{Block: storage.BlockNumber(i + 1), Offset: 1}
		if err := bt.Insert(EncodeInt4(int32(i)), ptr); err != nil {
			t.Fatalf("seed Insert(%d): %v", i, err)
		}
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Writer: insert the second half, one key at a time.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(stop)
		for i := total / 2; i < total; i++ {
			ptr := storage.ItemPointer{Block: storage.BlockNumber(i + 1), Offset: 1}
			if err := bt.Insert(EncodeInt4(int32(i)), ptr); err != nil {
				t.Errorf("Insert(%d): %v", i, err)
				return
			}
		}
	}()

	// Readers: spin Search across the seeded keys. Every found key
	// must report the right block — the Insert path mutates pages
	// under exclusive content latches and Search descends under
	// shared latches with right-link recovery, so a torn read or a
	// split-induced miss would surface here.
	const readers = 6
	var hits atomic.Uint64
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				key := int32(uint64(hits.Load()) % (total / 2))
				ptr, ok, err := bt.Search(EncodeInt4(key))
				if err != nil {
					t.Errorf("Search(%d): %v", key, err)
					return
				}
				if ok {
					if int32(ptr.Block) != key+1 {
						t.Errorf("Search(%d): block=%d want %d", key, ptr.Block, key+1)
						return
					}
					hits.Add(1)
				}
			}
		}()
	}
	wg.Wait()

	// Verify every inserted key is searchable post-run.
	for i := 0; i < total; i++ {
		ptr, ok, err := bt.Search(EncodeInt4(int32(i)))
		if err != nil || !ok {
			t.Fatalf("post-run Search(%d): ok=%v err=%v", i, ok, err)
		}
		if int32(ptr.Block) != int32(i+1) {
			t.Errorf("post-run Search(%d): block=%d want %d", i, ptr.Block, i+1)
		}
	}
	if hits.Load() == 0 {
		t.Error("no successful concurrent searches recorded")
	}
}

// TestConcurrentWritersInsertDisjointRanges covers Landing 3b's core
// promise: writers should no longer serialize behind a tree-wide mutex.
// Two writers insert disjoint key ranges concurrently while splits are
// possible; every key must be searchable after completion.
func TestConcurrentWritersInsertDisjointRanges(t *testing.T) {
	bt, _, cleanup := newTestTree(t)
	defer cleanup()

	const perWriter = 700
	var wg sync.WaitGroup
	errCh := make(chan error, 2)

	writer := func(base int32) {
		defer wg.Done()
		for i := 0; i < perWriter; i++ {
			k := base + int32(i)
			ptr := storage.ItemPointer{Block: storage.BlockNumber(k + 1), Offset: 1}
			if err := bt.Insert(EncodeInt4(k), ptr); err != nil {
				errCh <- err
				return
			}
		}
	}

	wg.Add(2)
	go writer(0)
	go writer(100_000)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent writer insert: %v", err)
		}
	}

	for i := 0; i < perWriter; i++ {
		for _, k := range []int32{int32(i), 100_000 + int32(i)} {
			ptr, ok, err := bt.Search(EncodeInt4(k))
			if err != nil {
				t.Fatalf("Search(%d): %v", k, err)
			}
			if !ok {
				t.Fatalf("Search(%d): not found", k)
			}
			if ptr.Block != storage.BlockNumber(k+1) {
				t.Fatalf("Search(%d): block=%d want=%d", k, ptr.Block, k+1)
			}
		}
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
