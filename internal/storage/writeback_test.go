package storage

import "testing"

// TestPoolBackendWritebackTriggersAtThreshold pins the M0122-0003 pg_stat_io
// follow-up: a backend's own dirty-victim-eviction writes must issue a real
// writeback (sync_file_range hint) once backend_flush_after pages have
// accumulated, and BackendWritebackCount() must reflect it. Mirrors
// TestBufferCountersDirtiedAndWritten's backend-eviction setup.
func TestPoolBackendWritebackTriggersAtThreshold(t *testing.T) {
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

	// Disabled (upstream default: backend_flush_after=0) must never issue a
	// writeback no matter how many pages are evicted.
	if got := pool.BackendWritebackCount(); got != 0 {
		t.Fatalf("BackendWritebackCount() = %d before any writes, want 0", got)
	}

	pool.SetBackendFlushAfter(2)

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
	for blk := BlockNumber(0); blk < BlockNumber(hotPages); blk++ {
		s, err := pool.Pin(BufferTag{Rel: hotRel, Block: blk})
		if err != nil {
			t.Fatalf("pin hot %d: %v", blk, err)
		}
		pool.MarkDirty(s)
		pool.Unpin(s)
	}
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

	if got := pool.BackendWritebackCount(); got == 0 {
		t.Errorf("BackendWritebackCount() = 0 after evicting %d dirty hot pages with backend_flush_after=2, want > 0 (unless this platform lacks sync_file_range)", hotPages)
	}
}

// TestPoolBackendWritebackOverrideTakesPrecedence pins the M0122-0003
// writeback follow-up: a non-nil BackendFlushAfterOverride hook (wired from
// the calling backend's own per-session `backend_flush_after`, since
// upstream's GUC is PGC_USERSET) must win over the process-wide
// SetBackendFlushAfter default set at boot, in both directions — a
// per-session override can enable writeback the process-wide default
// disables, and vice versa.
func TestPoolBackendWritebackOverrideTakesPrecedence(t *testing.T) {
	const poolSlots = 4
	const hotPages = 4

	newRig := func(t *testing.T) (*Pool, RelFileNode) {
		dir := t.TempDir()
		mgr := NewManager(ManagerConfig{DataDir: dir})
		pool, err := NewPool(mgr, PoolConfig{Slots: poolSlots})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = pool.Close(); _ = mgr.Close() })

		rel := RelFileNode{DBOid: 1, RelOid: 1, Fork: MainFork}
		seedPage := make(Page, BlockSize)
		if err := InitPage(seedPage); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < hotPages; i++ {
			if _, err := mgr.Extend(rel, seedPage); err != nil {
				t.Fatal(err)
			}
		}
		return pool, rel
	}
	dirtyAndEvictAll := func(t *testing.T, pool *Pool, rel RelFileNode) {
		for blk := BlockNumber(0); blk < BlockNumber(hotPages); blk++ {
			s, err := pool.Pin(BufferTag{Rel: rel, Block: blk})
			if err != nil {
				t.Fatalf("pin %d: %v", blk, err)
			}
			pool.MarkDirty(s)
			pool.Unpin(s)
		}
		// Evict everything by pinning a second, disjoint relation's worth of
		// cold pages through the same small pool (mirrors
		// TestPoolBackendWritebackTriggersAtThreshold's hot/cold setup).
		coldRel := RelFileNode{DBOid: 1, RelOid: 99, Fork: MainFork}
		seedPage := make(Page, BlockSize)
		_ = InitPage(seedPage)
		mgr := pool.mgr
		for i := 0; i < poolSlots*2; i++ {
			if _, err := mgr.Extend(coldRel, seedPage); err != nil {
				t.Fatal(err)
			}
		}
		for blk := BlockNumber(0); blk < BlockNumber(poolSlots*2); blk++ {
			s, err := pool.Pin(BufferTag{Rel: coldRel, Block: blk})
			if err != nil {
				continue
			}
			pool.Unpin(s)
		}
	}

	t.Run("override enables what the process-wide default disables", func(t *testing.T) {
		pool, rel := newRig(t)
		// Process-wide default stays 0 (disabled, upstream's own default);
		// only the per-session override turns writeback on.
		pool.BackendFlushAfterOverride = func() (int32, bool) { return 2, true }
		dirtyAndEvictAll(t, pool, rel)
		if got := pool.BackendWritebackCount(); got == 0 {
			t.Errorf("BackendWritebackCount() = 0 with an override of 2, want > 0")
		}
	})

	t.Run("override disables what the process-wide default enables", func(t *testing.T) {
		pool, rel := newRig(t)
		pool.SetBackendFlushAfter(2)
		pool.BackendFlushAfterOverride = func() (int32, bool) { return 0, true }
		dirtyAndEvictAll(t, pool, rel)
		if got := pool.BackendWritebackCount(); got != 0 {
			t.Errorf("BackendWritebackCount() = %d with an override of 0 overriding process-wide 2, want 0", got)
		}
	})

	t.Run("not-ok override falls back to the process-wide default", func(t *testing.T) {
		pool, rel := newRig(t)
		pool.SetBackendFlushAfter(2)
		pool.BackendFlushAfterOverride = func() (int32, bool) { return 99, false }
		dirtyAndEvictAll(t, pool, rel)
		if got := pool.BackendWritebackCount(); got == 0 {
			t.Errorf("BackendWritebackCount() = 0 with a not-ok override (should fall back to process-wide 2), want > 0")
		}
	})
}

// TestPoolBgwriterWritebackTriggersAtThreshold mirrors the backend test
// above for the bgwriter's WriteDirtyPages path.
func TestPoolBgwriterWritebackTriggersAtThreshold(t *testing.T) {
	const poolSlots = 8
	const dirtyPages = 4

	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	pool, err := NewPool(mgr, PoolConfig{Slots: poolSlots})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pool.Close(); _ = mgr.Close() }()

	pool.SetBgwriterFlushAfter(2)

	rel := RelFileNode{DBOid: 1, RelOid: 1, Fork: MainFork}
	seedPage := make(Page, BlockSize)
	if err := InitPage(seedPage); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < dirtyPages; i++ {
		if _, err := mgr.Extend(rel, seedPage); err != nil {
			t.Fatal(err)
		}
	}
	for blk := BlockNumber(0); blk < BlockNumber(dirtyPages); blk++ {
		s, err := pool.Pin(BufferTag{Rel: rel, Block: blk})
		if err != nil {
			t.Fatalf("pin %d: %v", blk, err)
		}
		pool.MarkDirty(s)
		pool.Unpin(s)
	}

	if n := pool.WriteDirtyPages(dirtyPages); n != dirtyPages {
		t.Fatalf("WriteDirtyPages wrote %d, want %d", n, dirtyPages)
	}

	if got := pool.BgwriterWritebackCount(); got == 0 {
		t.Errorf("BgwriterWritebackCount() = 0 after WriteDirtyPages flushed %d pages with bgwriter_flush_after=2, want > 0 (unless this platform lacks sync_file_range)", dirtyPages)
	}
}

// TestPoolCheckpointerWritebackTriggersAtThreshold mirrors the two tests
// above for FlushAllPaced (the checkpointer's path).
func TestPoolCheckpointerWritebackTriggersAtThreshold(t *testing.T) {
	const poolSlots = 8
	const dirtyPages = 4

	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	pool, err := NewPool(mgr, PoolConfig{Slots: poolSlots})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pool.Close(); _ = mgr.Close() }()

	pool.SetCheckpointFlushAfter(2)

	rel := RelFileNode{DBOid: 1, RelOid: 1, Fork: MainFork}
	seedPage := make(Page, BlockSize)
	if err := InitPage(seedPage); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < dirtyPages; i++ {
		if _, err := mgr.Extend(rel, seedPage); err != nil {
			t.Fatal(err)
		}
	}
	for blk := BlockNumber(0); blk < BlockNumber(dirtyPages); blk++ {
		s, err := pool.Pin(BufferTag{Rel: rel, Block: blk})
		if err != nil {
			t.Fatalf("pin %d: %v", blk, err)
		}
		pool.MarkDirty(s)
		pool.Unpin(s)
	}

	if err := pool.FlushAllPaced(nil); err != nil {
		t.Fatal(err)
	}

	if got := pool.CheckpointWritebackCount(); got == 0 {
		t.Errorf("CheckpointWritebackCount() = 0 after FlushAllPaced flushed %d pages with checkpoint_flush_after=2, want > 0 (unless this platform lacks sync_file_range)", dirtyPages)
	}
}

// TestSyncFileRangeHintOnRealFile is a narrow smoke test for the raw
// platform hook itself, independent of the Pool-level threshold logic.
func TestSyncFileRangeHintOnRealFile(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	defer func() { _ = mgr.Close() }()

	rel := RelFileNode{DBOid: 1, RelOid: 1, Fork: MainFork}
	seedPage := make(Page, BlockSize)
	if err := InitPage(seedPage); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Extend(rel, seedPage); err != nil {
		t.Fatal(err)
	}

	if err := mgr.SyncFileRangeHint(rel); err != nil {
		t.Fatalf("SyncFileRangeHint on a real just-extended file: %v", err)
	}
}
