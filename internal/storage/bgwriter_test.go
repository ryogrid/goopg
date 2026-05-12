package storage

import (
	"testing"
	"time"
)

// newBgwriterPool creates a pool + manager suitable for bgwriter tests.
func newBgwriterPool(t *testing.T, slots int) (*Pool, RelFileNode) {
	t.Helper()
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	pool, err := NewPool(mgr, PoolConfig{Slots: slots})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close(); _ = mgr.Close() })
	rel := RelFileNode{DBOid: 1, RelOid: 1, Fork: MainFork}
	return pool, rel
}

// TestBgwriterFlushesPages verifies that WriteDirtyPages clears the dirty
// flag on pages it successfully writes.
func TestBgwriterFlushesPages(t *testing.T) {
	const nPages = 20
	pool, rel := newBgwriterPool(t, nPages+8)

	seedPage := make(Page, BlockSize)
	if err := InitPage(seedPage); err != nil {
		t.Fatal(err)
	}
	// Extend the relation with nPages blocks.
	for i := 0; i < nPages; i++ {
		if _, err := pool.Manager().Extend(rel, seedPage); err != nil {
			t.Fatal(err)
		}
	}

	// Pin each page and mark dirty.
	for blk := BlockNumber(0); blk < BlockNumber(nPages); blk++ {
		slot, err := pool.Pin(BufferTag{Rel: rel, Block: blk})
		if err != nil {
			t.Fatalf("Pin %d: %v", blk, err)
		}
		pool.MarkDirty(slot)
		pool.Unpin(slot)
	}

	// Verify all are dirty before bgwriter runs.
	pool.evictMu.Lock()
	dirty := 0
	for _, s := range pool.slots {
		if s.valid && s.dirty {
			dirty++
		}
	}
	pool.evictMu.Unlock()
	if dirty == 0 {
		t.Fatal("expected dirty pages before bgwriter flush")
	}

	// Run WriteDirtyPages with a large limit.
	written := pool.WriteDirtyPages(nPages * 2)
	if written == 0 {
		t.Error("WriteDirtyPages wrote 0 pages; expected > 0")
	}

	// Pages should now be clean.
	pool.evictMu.Lock()
	dirtyAfter := 0
	for _, s := range pool.slots {
		if s.valid && s.dirty {
			dirtyAfter++
		}
	}
	pool.evictMu.Unlock()
	if dirtyAfter != 0 {
		t.Errorf("expected 0 dirty pages after flush, got %d", dirtyAfter)
	}
}

// TestBgwriterGoroutine verifies the Bgwriter goroutine starts and stops
// cleanly and proactively flushes dirty pages over time.
func TestBgwriterGoroutine(t *testing.T) {
	const nPages = 10
	pool, rel := newBgwriterPool(t, nPages+8)

	seedPage := make(Page, BlockSize)
	if err := InitPage(seedPage); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < nPages; i++ {
		if _, err := pool.Manager().Extend(rel, seedPage); err != nil {
			t.Fatal(err)
		}
	}

	// Dirty all pages.
	for blk := BlockNumber(0); blk < BlockNumber(nPages); blk++ {
		slot, err := pool.Pin(BufferTag{Rel: rel, Block: blk})
		if err != nil {
			t.Fatalf("Pin %d: %v", blk, err)
		}
		pool.MarkDirty(slot)
		pool.Unpin(slot)
	}

	// Start bgwriter with fast tick.
	bg := NewBgwriter(pool, 20*time.Millisecond, 100)
	bg.Start()

	// Wait for several ticks.
	time.Sleep(100 * time.Millisecond)

	// Stop bgwriter.
	bg.Stop()

	// Verify pages were cleaned.
	pool.evictMu.Lock()
	remaining := 0
	for _, s := range pool.slots {
		if s.valid && s.dirty {
			remaining++
		}
	}
	pool.evictMu.Unlock()
	if remaining != 0 {
		t.Errorf("bgwriter did not flush all pages; %d dirty pages remain", remaining)
	}
}

// TestBgwriterDoDDirtyVictimRate is the DoD test for M0048-0003.
//
// Setup:
//   - Pool with 64 slots, bgwriter with 5ms tick
//   - Create 50 "hot" pages (marked dirty)
//   - Start bgwriter; wait for it to clean the dirty pages
//   - Pin+unpin all 50 pages to warm them in pool (usageCount>0)
//   - Then run 200 evictions by forcing cache misses on a second relation
//   - Verify dirty-victim rate ≤ 5%
//
// Without the bgwriter the first 50 evictions hit dirty pages (78% dirty
// rate). With the bgwriter having flushed them first, the rate drops to 0%.
func TestBgwriterDoDDirtyVictimRate(t *testing.T) {
	const poolSlots = 64
	const hotPages = 50
	const evictions = 200

	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	pool, err := NewPool(mgr, PoolConfig{Slots: poolSlots})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pool.Close(); _ = mgr.Close() }()

	hotRel := RelFileNode{DBOid: 1, RelOid: 1, Fork: MainFork}
	coldRel := RelFileNode{DBOid: 1, RelOid: 2, Fork: MainFork}

	seedPage := make(Page, BlockSize)
	if err := InitPage(seedPage); err != nil {
		t.Fatal(err)
	}

	// Create hotRel pages and make them dirty.
	for i := 0; i < hotPages; i++ {
		if _, err := mgr.Extend(hotRel, seedPage); err != nil {
			t.Fatal(err)
		}
	}
	for blk := BlockNumber(0); blk < BlockNumber(hotPages); blk++ {
		s, err := pool.Pin(BufferTag{Rel: hotRel, Block: blk})
		if err != nil {
			t.Fatalf("pin hot %d: %v", blk, err)
		}
		pool.MarkDirty(s)
		pool.Unpin(s)
	}

	// Start bgwriter with fast tick to clean dirty pages.
	bg := NewBgwriter(pool, 5*time.Millisecond, 200)
	bg.Start()
	time.Sleep(50 * time.Millisecond) // wait for bgwriter to flush
	bg.Stop()

	// Re-pin hot pages so they have high usageCount (makes them harder to evict).
	for blk := BlockNumber(0); blk < BlockNumber(hotPages); blk++ {
		s, err := pool.Pin(BufferTag{Rel: hotRel, Block: blk})
		if err != nil {
			t.Fatalf("re-pin hot %d: %v", blk, err)
		}
		pool.Unpin(s)
	}

	// Reset victim stats.
	pool.ResetVictimStats()

	// Create coldRel pages (more than pool capacity to force evictions).
	for i := 0; i < evictions; i++ {
		if _, err := mgr.Extend(coldRel, seedPage); err != nil {
			t.Fatal(err)
		}
	}

	// Pin each cold page, forcing evictions.
	for blk := BlockNumber(0); blk < BlockNumber(evictions); blk++ {
		s, err := pool.Pin(BufferTag{Rel: coldRel, Block: blk})
		if err != nil {
			continue
		}
		pool.Unpin(s)
	}

	rate := pool.DirtyVictimRate()
	t.Logf("dirty victim rate: %.1f%%", rate*100)

	// DoD: ≤ 5% of evictions should encounter dirty pages.
	const maxDirtyRate = 0.05
	if rate > maxDirtyRate {
		t.Errorf("dirty victim rate %.1f%% > %.0f%% limit — bgwriter not cleaning pages effectively",
			rate*100, maxDirtyRate*100)
	}
}

// TestBgwriterMaxPagesLimit verifies that WriteDirtyPages respects the
// maxPages cap per tick.
func TestBgwriterMaxPagesLimit(t *testing.T) {
	const nPages = 30
	const maxPerTick = 10
	pool, rel := newBgwriterPool(t, nPages+8)

	seedPage := make(Page, BlockSize)
	if err := InitPage(seedPage); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < nPages; i++ {
		if _, err := pool.Manager().Extend(rel, seedPage); err != nil {
			t.Fatal(err)
		}
	}
	for blk := BlockNumber(0); blk < BlockNumber(nPages); blk++ {
		s, err := pool.Pin(BufferTag{Rel: rel, Block: blk})
		if err != nil {
			t.Fatalf("Pin %d: %v", blk, err)
		}
		pool.MarkDirty(s)
		pool.Unpin(s)
	}

	written := pool.WriteDirtyPages(maxPerTick)
	if written > maxPerTick {
		t.Errorf("WriteDirtyPages wrote %d pages, limit was %d", written, maxPerTick)
	}
}
