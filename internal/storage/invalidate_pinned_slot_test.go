package storage

import (
	"testing"
	"time"
)

// TestTruncateWaitsOutPinnedDirtyPage is the regression gate for
// AI-20261103-004134-005 (nightly pgbench "flush victim: write …/16406 blk
// 677: out of range (nblocks=50)").
//
// A dirty page of the truncated relation was pinned — by the bgwriter,
// checkpointer or an autovacuum pass — at the moment TRUNCATE invalidated the
// relation's buffers. InvalidateRel skipped pinned slots (and dropped any slot
// whose state CAS lost a race), so the page stayed valid+dirty under its old
// tag; the relation was then re-extended (pgbench's next workload appends to
// pgbench_history) and the stale page's eviction flush wrote block 5 into a
// 2-block file. PG's InvalidateBuffer (bufmgr.c) waits for the pin to drop
// and retries; TRUNCATE now does the same (InvalidateRelForTruncate).
func TestTruncateWaitsOutPinnedDirtyPage(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	pool, err := NewPool(mgr, PoolConfig{Slots: 4})
	if err != nil {
		t.Fatal(err)
	}
	relA := RelFileNode{DBOid: 1, RelOid: 7411, Fork: MainFork}
	relB := RelFileNode{DBOid: 1, RelOid: 7412, Fork: MainFork}

	// Six blocks of A on disk; block 5 stays pinned (the concurrent pin
	// holder).
	var held *Slot
	for blk := BlockNumber(0); blk < 6; blk++ {
		s, n, err := pool.PinNew(relA)
		if err != nil {
			t.Fatal(err)
		}
		if n != blk {
			t.Fatalf("PinNew gave block %d, want %d", n, blk)
		}
		pool.MarkDirty(s)
		if blk == 5 {
			held = s
			continue
		}
		pool.Unpin(s)
	}

	done := make(chan error, 1)
	go func() {
		// TRUNCATE: invalidate buffers, then the smgr truncate.
		pool.InvalidateRelForTruncate(relA)
		done <- mgr.TruncateRelation(relA)
	}()
	time.Sleep(30 * time.Millisecond)
	// The holder dirties the page (hint bits, a vacuum pass) and releases it.
	pool.MarkDirty(held)
	pool.Unpin(held)
	if err := <-done; err != nil {
		t.Fatalf("TruncateRelation: %v", err)
	}

	// The next workload re-extends A to two blocks.
	for i := 0; i < 2; i++ {
		s, _, err := pool.PinNew(relA)
		if err != nil {
			t.Fatalf("re-extend A: %v", err)
		}
		pool.MarkDirty(s)
		pool.Unpin(s)
	}
	// Cycle every slot through eviction: a surviving stale (A, 5) page would
	// be flushed past A's new end.
	for i := 0; i < 8; i++ {
		s, _, err := pool.PinNew(relB)
		if err != nil {
			t.Fatalf("PinNew(relB) #%d: %v (a pinned dirty page of the truncated relation survived InvalidateRel)", i, err)
		}
		pool.MarkDirty(s)
		pool.Unpin(s)
	}
	if n, err := mgr.NBlocks(relA); err != nil || n != 2 {
		t.Fatalf("relA has %d blocks (err %v), want 2", n, err)
	}
}
