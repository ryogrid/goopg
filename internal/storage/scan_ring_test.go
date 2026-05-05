package storage

import (
	"testing"
)

// TestScanRingBasic verifies that AcquirePage returns a readable page,
// ReleasePage releases the pool lock/pin, and Close is idempotent.
func TestScanRingBasic(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	t.Cleanup(func() { _ = mgr.Close() })

	pool, err := NewPool(mgr, PoolConfig{Slots: 64})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })

	rel := RelFileNode{DBOid: 1, RelOid: 1, Fork: MainFork}
	seedPage := make(Page, BlockSize)
	if err := InitPage(seedPage); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Extend(rel, seedPage); err != nil {
		t.Fatal(err)
	}

	ring := NewScanRing(pool, rel)

	page, err := ring.AcquirePage(0)
	if err != nil {
		t.Fatalf("AcquirePage(0): %v", err)
	}
	if page == nil {
		t.Fatal("AcquirePage returned nil page")
	}

	ring.ReleasePage()
	if ring.ActivePage() != nil {
		t.Error("ActivePage should be nil after ReleasePage")
	}

	ring.Close() // idempotent
}

// TestScanRingCacheHitUsesPool verifies that when a block is already in the
// pool, ScanRing returns that cached page (pool slot used) rather than doing
// a fresh disk read into the ring buffer.
func TestScanRingCacheHitUsesPool(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	t.Cleanup(func() { _ = mgr.Close() })

	pool, err := NewPool(mgr, PoolConfig{Slots: 64})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })

	rel := RelFileNode{DBOid: 1, RelOid: 2, Fork: MainFork}
	seedPage := make(Page, BlockSize)
	if err := InitPage(seedPage); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Extend(rel, seedPage); err != nil {
		t.Fatal(err)
	}

	// Pre-load block 0 into the pool.
	slot, err := pool.Pin(BufferTag{Rel: rel, Block: 0})
	if err != nil {
		t.Fatal(err)
	}
	pool.Unpin(slot)

	// Count reads: with cache hit the ring should not read from disk.
	var diskReads int
	pool.OnPinWait = func() { diskReads++ }

	ring := NewScanRing(pool, rel)
	page, err := ring.AcquirePage(0)
	if err != nil {
		t.Fatalf("AcquirePage: %v", err)
	}
	if page == nil {
		t.Fatal("nil page")
	}
	ring.ReleasePage()

	// OnPinWait fires only when Pin does a disk read. No disk read expected.
	if diskReads != 0 {
		t.Errorf("expected 0 disk reads for cache hit, got %d", diskReads)
	}
}

// TestScanRingCacheMissNoEviction is the DoD test for M0048-0002.
//
// Setup:
//   - Pool with 100 slots
//   - 90 "hot" pages pre-loaded (rel A, blocks 0-89)
//   - Scan rel B with 500 blocks using ScanRing (ring size 32)
//
// Expected: ≥ 95% of the 90 hot pages (≥ 86 pages) remain in the pool
// after the scan. Without the ring strategy, the 500-page scan would
// evict essentially all hot pages; with the ring, cache misses are
// served from private buffers so hot pages are never evicted.
func TestScanRingCacheMissNoEviction(t *testing.T) {
	const poolSlots = 100
	const hotPages = 90
	const scanPages = 500

	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	t.Cleanup(func() { _ = mgr.Close() })

	pool, err := NewPool(mgr, PoolConfig{Slots: poolSlots})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })

	hotRel := RelFileNode{DBOid: 1, RelOid: 10, Fork: MainFork}
	scanRel := RelFileNode{DBOid: 1, RelOid: 20, Fork: MainFork}

	seedPage := make(Page, BlockSize)
	if err := InitPage(seedPage); err != nil {
		t.Fatal(err)
	}

	// Create and pre-load hotPages pages of hotRel into the pool.
	for i := 0; i < hotPages; i++ {
		if _, err := mgr.Extend(hotRel, seedPage); err != nil {
			t.Fatal(err)
		}
	}
	for blk := BlockNumber(0); blk < BlockNumber(hotPages); blk++ {
		s, err := pool.Pin(BufferTag{Rel: hotRel, Block: blk})
		if err != nil {
			t.Fatalf("pre-load hot page %d: %v", blk, err)
		}
		pool.Unpin(s)
	}

	// Verify all hot pages are in pool before the scan.
	hotBefore := countCachedBlocks(pool, hotRel, hotPages)
	if hotBefore != hotPages {
		t.Fatalf("expected %d hot pages cached before scan, got %d", hotPages, hotBefore)
	}

	// Create scan relation with scanPages blocks.
	for i := 0; i < scanPages; i++ {
		if _, err := mgr.Extend(scanRel, seedPage); err != nil {
			t.Fatal(err)
		}
	}

	// Scan scanRel using ScanRing (cache misses go to private ring buffers).
	ring := NewScanRing(pool, scanRel)
	for blk := BlockNumber(0); blk < BlockNumber(scanPages); blk++ {
		page, err := ring.AcquirePage(blk)
		if err != nil {
			t.Fatalf("AcquirePage(%d): %v", blk, err)
		}
		_ = page
		ring.ReleasePage()
	}
	ring.Close()

	// Count how many hot pages survived the scan.
	hotAfter := countCachedBlocks(pool, hotRel, hotPages)
	hitRate := float64(hotAfter) / float64(hotPages)
	t.Logf("hot pages after scan: %d / %d  (hit rate %.1f%%)",
		hotAfter, hotPages, hitRate*100)

	// DoD: ≥ 95% of hot pages must still be cached.
	const minHitRate = 0.95
	if hitRate < minHitRate {
		t.Errorf("hot-page hit rate %.1f%% < required %.0f%% — ring strategy not protecting hot pages",
			hitRate*100, minHitRate*100)
	}
}

// countCachedBlocks reports how many of the first nBlocks blocks of rel
// are currently in the pool's byTag map.
func countCachedBlocks(pool *Pool, rel RelFileNode, nBlocks int) int {
	pool.poolMu.Lock()
	defer pool.poolMu.Unlock()
	count := 0
	for blk := 0; blk < nBlocks; blk++ {
		if _, ok := pool.byTag[BufferTag{Rel: rel, Block: BlockNumber(blk)}]; ok {
			count++
		}
	}
	return count
}

// TestScanRingMultiBlock verifies that the ring correctly cycles through
// its 32 private buffers and can serve more than 32 blocks without error.
func TestScanRingMultiBlock(t *testing.T) {
	const nBlocks = 100

	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	t.Cleanup(func() { _ = mgr.Close() })

	pool, err := NewPool(mgr, PoolConfig{Slots: 32})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })

	rel := RelFileNode{DBOid: 1, RelOid: 3, Fork: MainFork}
	seedPage := make(Page, BlockSize)
	if err := InitPage(seedPage); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < nBlocks; i++ {
		if _, err := mgr.Extend(rel, seedPage); err != nil {
			t.Fatal(err)
		}
	}

	ring := NewScanRing(pool, rel)
	for blk := BlockNumber(0); blk < BlockNumber(nBlocks); blk++ {
		page, err := ring.AcquirePage(blk)
		if err != nil {
			t.Fatalf("AcquirePage(%d): %v", blk, err)
		}
		if page == nil {
			t.Fatalf("block %d: nil page", blk)
		}
		ring.ReleasePage()
	}
	ring.Close()
}
