package storage

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestBMIOInProgressSingleRead is the DoD test for M0048-0001.
//
// 64 goroutines simultaneously call pool.Pin for the same block that is
// not yet cached. With BM_IO_IN_PROGRESS, only ONE goroutine should
// issue the disk read (smgr.ReadBlock call); the other 63 should wait
// on p.ioCond and then pick up the cached result.
//
// We instrument ReadBlock via a slow Manager wrapper that counts calls
// and adds a small sleep so the other goroutines can accumulate before
// the read completes.
func TestBMIOInProgressSingleRead(t *testing.T) {
	const nGoroutines = 64
	const sleepDuration = 5 * time.Millisecond // let all goroutines reach Pin

	dir := t.TempDir()
	var readCount atomic.Int64

	// Wrap Manager to count and slow down ReadBlock calls.
	baseMgr := NewManager(ManagerConfig{DataDir: dir})
	t.Cleanup(func() { _ = baseMgr.Close() })

	// Create the relation file and write block 0 so ReadBlock succeeds.
	rel := RelFileNode{DBOid: 1, RelOid: 1, Fork: MainFork}
	seedPage := make(Page, BlockSize)
	if err := InitPage(seedPage); err != nil {
		t.Fatal(err)
	}
	// Extend the file to 1 block so Pin knows the block exists.
	if _, err := baseMgr.Extend(rel, seedPage); err != nil {
		t.Fatal(err)
	}

	// Pool with enough slots for all goroutines + some extra.
	pool, err := NewPool(baseMgr, PoolConfig{Slots: nGoroutines + 16})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })

	// Install OnPinWait hook to count actual disk reads and insert a
	// sleep so all 64 goroutines can reach Pin before the first read
	// completes.
	pool.OnPinWait = func() {
		readCount.Add(1)
		time.Sleep(sleepDuration)
	}

	// Warm start: ensure block 0 is NOT in the buffer pool (fresh pool).
	tag := BufferTag{Rel: rel, Block: 0}

	var wg sync.WaitGroup
	wg.Add(nGoroutines)
	errors := make([]error, nGoroutines)

	for i := 0; i < nGoroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			s, err := pool.Pin(tag)
			if err != nil {
				errors[idx] = err
				return
			}
			pool.Unpin(s)
		}(i)
	}
	wg.Wait()

	// Check for errors.
	for i, err := range errors {
		if err != nil {
			t.Errorf("goroutine %d: Pin error: %v", i, err)
		}
	}

	got := readCount.Load()
	if got != 1 {
		t.Errorf("smgr.Read invocation count = %d, want 1 (BM_IO_IN_PROGRESS should deduplicate reads)", got)
	}
}

// TestBMIOInProgressDistinctBlocks verifies that concurrent reads to
// DIFFERENT blocks still each issue their own disk read — the dedup
// only applies to the same tag.
func TestBMIOInProgressDistinctBlocks(t *testing.T) {
	const nBlocks = 8

	dir := t.TempDir()
	var readCount atomic.Int64

	baseMgr := NewManager(ManagerConfig{DataDir: dir})
	t.Cleanup(func() { _ = baseMgr.Close() })

	rel := RelFileNode{DBOid: 1, RelOid: 2, Fork: MainFork}
	seedPage := make(Page, BlockSize)
	if err := InitPage(seedPage); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < nBlocks; i++ {
		if _, err := baseMgr.Extend(rel, seedPage); err != nil {
			t.Fatal(err)
		}
	}

	pool, err := NewPool(baseMgr, PoolConfig{Slots: nBlocks + 8})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })

	pool.OnPinWait = func() {
		readCount.Add(1)
	}

	var wg sync.WaitGroup
	wg.Add(nBlocks)
	for i := 0; i < nBlocks; i++ {
		go func(blk BlockNumber) {
			defer wg.Done()
			s, err := pool.Pin(BufferTag{Rel: rel, Block: blk})
			if err == nil {
				pool.Unpin(s)
			}
		}(BlockNumber(i))
	}
	wg.Wait()

	got := readCount.Load()
	if got != nBlocks {
		t.Errorf("distinct-block read count = %d, want %d", got, nBlocks)
	}
}

// TestBMIOInProgressRaceCondition is a race-detector stress test:
// repeated concurrent Pins on the same block with a rapidly evicting pool.
// If BM_IO_IN_PROGRESS is implemented correctly, the race detector should
// find no data races.
func TestBMIOInProgressRaceCondition(t *testing.T) {
	const nGoroutines = 16
	const nIterations = 50

	dir := t.TempDir()
	baseMgr := NewManager(ManagerConfig{DataDir: dir})
	t.Cleanup(func() { _ = baseMgr.Close() })

	rel := RelFileNode{DBOid: 1, RelOid: 3, Fork: MainFork}
	seedPage := make(Page, BlockSize)
	if err := InitPage(seedPage); err != nil {
		t.Fatal(err)
	}
	if _, err := baseMgr.Extend(rel, seedPage); err != nil {
		t.Fatal(err)
	}

	// Very small pool (4 slots) to force frequent evictions.
	pool, err := NewPool(baseMgr, PoolConfig{Slots: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })

	tag := BufferTag{Rel: rel, Block: 0}
	var wg sync.WaitGroup
	wg.Add(nGoroutines)
	for i := 0; i < nGoroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < nIterations; j++ {
				s, err := pool.Pin(tag)
				if err != nil {
					continue
				}
				pool.Unpin(s)
			}
		}()
	}
	wg.Wait()
}
