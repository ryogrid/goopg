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

// TestBufferCountersEvictionAndExtend pins the M0122-0003 pg_stat_io
// follow-up: storage.Pool must also track evictions / extends (backing
// pg_stat_io's evictions and extends/extend_bytes columns), mirroring
// bufmgr.c's shared_blks_evicted / shared_blks_extend accounting.
// extendCount advances once per PinNew relation extension; evictionCount
// advances once per real victim eviction (a valid tag actually displaced
// from a slot), regardless of whether the victim was dirty.
func TestBufferCountersEvictionAndExtend(t *testing.T) {
	const poolSlots = 2

	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	pool, err := NewPool(mgr, PoolConfig{Slots: poolSlots})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pool.Close(); _ = mgr.Close() }()

	rel := RelFileNode{DBOid: 1, RelOid: 1, Fork: MainFork}

	if got := pool.EvictionCount(); got != 0 {
		t.Fatalf("EvictionCount before any activity = %d, want 0", got)
	}
	if got := pool.ExtendCount(); got != 0 {
		t.Fatalf("ExtendCount before any activity = %d, want 0", got)
	}

	// Filling the pool's own slot count via PinNew must not evict anything
	// (every slot starts free) but must advance extendCount once per call.
	for i := 0; i < poolSlots; i++ {
		s, _, err := pool.PinNew(rel)
		if err != nil {
			t.Fatalf("PinNew %d: %v", i, err)
		}
		pool.Unpin(s)
	}
	if got := pool.ExtendCount(); got != poolSlots {
		t.Errorf("ExtendCount after %d PinNew calls = %d, want %d", poolSlots, got, poolSlots)
	}
	if got := pool.EvictionCount(); got != 0 {
		t.Errorf("EvictionCount after filling empty slots = %d, want 0 (nothing evicted yet)", got)
	}

	// One more PinNew than the pool has slots forces a real eviction.
	const extra = 3
	for i := 0; i < extra; i++ {
		s, _, err := pool.PinNew(rel)
		if err != nil {
			t.Fatalf("PinNew extra %d: %v", i, err)
		}
		pool.Unpin(s)
	}
	if got, want := pool.ExtendCount(), int64(poolSlots+extra); got != want {
		t.Errorf("ExtendCount after %d more PinNew calls = %d, want %d", extra, got, want)
	}
	if got := pool.EvictionCount(); got != extra {
		t.Errorf("EvictionCount after forcing %d evictions = %d, want %d", extra, got, extra)
	}
}

// TestPoolReadTimeNanosAccumulates pins the M0122-0003 track_io_timing
// follow-up: Pool.AddReadTimeNanos/ReadTimeNanos back pg_stat_io's
// read_time column. storage.Pool itself never calls AddReadTimeNanos
// directly (that only happens from the OnPinDone closure wired in
// internal/initdb/open.go, gated on the pinning backend's track_io_timing
// flag) — this test exercises the counter in isolation, mirroring how
// BufferCounters' hit/read/dirtied/written pairs are unit-tested apart from
// their real call sites.
func TestPoolReadTimeNanosAccumulates(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	pool, err := NewPool(mgr, PoolConfig{Slots: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pool.Close(); _ = mgr.Close() }()

	if got := pool.ReadTimeNanos(); got != 0 {
		t.Fatalf("ReadTimeNanos before any accumulation = %d, want 0", got)
	}
	pool.AddReadTimeNanos(1_000_000)
	pool.AddReadTimeNanos(2_000_000)
	if got, want := pool.ReadTimeNanos(), int64(3_000_000); got != want {
		t.Errorf("ReadTimeNanos after two adds = %d, want %d", got, want)
	}
	// Non-positive durations (WaitEventEnd's zero-guard for a mismatched or
	// clock-skewed pair) must not be recorded.
	pool.AddReadTimeNanos(0)
	pool.AddReadTimeNanos(-5)
	if got, want := pool.ReadTimeNanos(), int64(3_000_000); got != want {
		t.Errorf("ReadTimeNanos after non-positive adds = %d, want unchanged %d", got, want)
	}
}
