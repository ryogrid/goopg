package initdb

import (
	"path/filepath"
	"testing"
	"time"
)

// TestApplyWriterSettingsReachesRunningLoops pins M0122-0008 slice 2.
//
// PG 18.3 declares wal_writer_delay, wal_writer_flush_after, bgwriter_delay
// and bgwriter_lru_maxpages PGC_SIGHUP (guc_tables.c): the walwriter and the
// bgwriter re-read them after a reload. goopg read them once, when Open
// started the loops, and registered them PGC_POSTMASTER. commit_delay and
// commit_siblings were already reloadable in the registry, but nothing pushed
// a reloaded value into the WAL writer. The reload hook now calls
// ApplyWriterSettings, which must reach the loops that are already running.
func TestApplyWriterSettingsReachesRunningLoops(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(OpenOptions{
		DataDir:              dir,
		PoolSlots:            64,
		WalWriterDelay:       200 * time.Millisecond,
		WalWriterFlushAfter:  1 << 20,
		BgwriterDelay:        200 * time.Millisecond,
		BgwriterMaxPages:     100,
		CheckpointFlushAfter: 32,
		BgwriterFlushAfter:   64,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if rt.Bgwriter() == nil {
		t.Fatal("bgwriter not started")
	}

	rt.ApplyWriterSettings(WriterSettings{
		WalWriterDelay:       50 * time.Millisecond,
		WalWriterFlushAfter:  64 << 10,
		CommitDelayUs:        10,
		CommitSiblings:       7,
		BgwriterDelay:        30 * time.Millisecond,
		BgwriterMaxPages:     0,
		CheckpointFlushAfter: 128,
		BgwriterFlushAfter:   0,
	})
	if got := rt.WalWriterDelay(); got != 50*time.Millisecond {
		t.Errorf("wal_writer_delay = %v, want 50ms", got)
	}
	if got := rt.WAL.WalWriterFlushAfter(); got != 64<<10 {
		t.Errorf("wal_writer_flush_after = %d, want %d", got, 64<<10)
	}
	if got := rt.WAL.CommitDelayUs(); got != 10 {
		t.Errorf("commit_delay = %d, want 10", got)
	}
	if got := rt.WAL.CommitSiblings(); got != 7 {
		t.Errorf("commit_siblings = %d, want 7", got)
	}
	if got := rt.Bgwriter().Delay(); got != 30*time.Millisecond {
		t.Errorf("bgwriter_delay = %v, want 30ms", got)
	}
	if got := rt.Bgwriter().MaxPages(); got != 0 {
		t.Errorf("bgwriter_lru_maxpages = %d, want 0", got)
	}
	// checkpoint_flush_after and bgwriter_flush_after were reloadable in the
	// registry but only Open set the pool's thresholds (slice 3).
	if got := rt.Pool.CheckpointFlushAfter(); got != 128 {
		t.Errorf("checkpoint_flush_after = %d, want 128", got)
	}
	if got := rt.Pool.BgwriterFlushAfter(); got != 0 {
		t.Errorf("bgwriter_flush_after = %d, want 0", got)
	}
}

// TestBgwriterRunsWithZeroLRUMaxPages: bgwriter_lru_maxpages = 0 stops the
// LRU writes but PG's bgwriter process keeps running, so a reload can raise
// the value again. goopg used to skip starting the goroutine at all.
func TestBgwriterRunsWithZeroLRUMaxPages(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(OpenOptions{
		DataDir:          dir,
		PoolSlots:        64,
		BgwriterDelay:    50 * time.Millisecond,
		BgwriterMaxPages: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if rt.Bgwriter() == nil {
		t.Fatal("bgwriter_lru_maxpages = 0 left no bgwriter for a reload to reach")
	}
	rt.ApplyWriterSettings(WriterSettings{BgwriterMaxPages: 100})
	if got := rt.Bgwriter().MaxPages(); got != 100 {
		t.Errorf("bgwriter_lru_maxpages = %d after reload, want 100", got)
	}
}
