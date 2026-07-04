package storage

import "testing"

// TestBufferCountersDirtiedAndWritten pins the M0122-0003 BUFFERS follow-up:
// storage.Pool.BufferCounters() must also track shared_blks_dirtied /
// shared_blks_written (mirrors bufmgr.c's MarkBufferDirty / FlushBuffer
// accounting), not just the pre-existing hit/read pair.
//
// Setup mirrors TestBgwriterDoDDirtyVictimRate's pattern for forcing a
// backend-driven eviction: a tiny pool, a handful of dirtied "hot" pages,
// then enough "cold" pages pinned to force the hot pages out.
func TestBufferCountersDirtiedAndWritten(t *testing.T) {
	const poolSlots = 4
	const hotPages = 4
	const coldPages = 8

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

	for i := 0; i < hotPages; i++ {
		if _, err := mgr.Extend(hotRel, seedPage); err != nil {
			t.Fatal(err)
		}
	}

	_, _, dirtiedBefore, writtenBefore := pool.BufferCounters()

	// Dirty every hot page: each MarkDirty is a clean->dirty transition,
	// so dirtiedCount must advance by exactly hotPages.
	for blk := BlockNumber(0); blk < BlockNumber(hotPages); blk++ {
		s, err := pool.Pin(BufferTag{Rel: hotRel, Block: blk})
		if err != nil {
			t.Fatalf("pin hot %d: %v", blk, err)
		}
		pool.MarkDirty(s)
		// A second MarkDirty on the same (already-dirty) slot must NOT
		// double-count.
		pool.MarkDirty(s)
		pool.Unpin(s)
	}

	_, _, dirtiedAfterMark, writtenAfterMark := pool.BufferCounters()
	if got := dirtiedAfterMark - dirtiedBefore; got != hotPages {
		t.Errorf("dirtiedCount advanced by %d, want %d (exactly one per page, no double-count)", got, hotPages)
	}
	if writtenAfterMark != writtenBefore {
		t.Errorf("writtenCount advanced by MarkDirty alone (no eviction happened yet): %d -> %d", writtenBefore, writtenAfterMark)
	}

	// Force every hot page out via cold-page evictions (pool has only
	// poolSlots slots, all currently pinned-then-unpinned hot pages).
	for i := 0; i < coldPages; i++ {
		if _, err := mgr.Extend(coldRel, seedPage); err != nil {
			t.Fatal(err)
		}
	}
	for blk := BlockNumber(0); blk < BlockNumber(coldPages); blk++ {
		s, err := pool.Pin(BufferTag{Rel: coldRel, Block: blk})
		if err != nil {
			continue
		}
		pool.Unpin(s)
	}

	_, _, _, writtenAfterEvict := pool.BufferCounters()
	if writtenAfterEvict <= writtenAfterMark {
		t.Errorf("writtenCount should advance once dirty hot pages are evicted to make room: %d -> %d", writtenAfterMark, writtenAfterEvict)
	}
}
