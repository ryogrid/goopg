package storage

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestTruncateDuringEvictionFlushDiscardsStalePage is the regression gate for
// AI-20261031-002109-005 (nightly pgbench "flush victim: write … blk N: out
// of range (nblocks=0)").
//
// A TRUNCATE (pgbench's per-workload `truncate pgbench_history`) and a buffer
// eviction of a dirty page of that same relation race: claimVictim has already
// cleared valid+dirty, so InvalidateRel's bit-CAS cannot reach the slot, and
// the in-flight flush proceeds to write a block that the concurrent truncate
// has removed — goopg's smgr rejects the write (writeBlock nblocks gate) and
// the client's next statement dies. PostgreSQL cannot reach this state:
// TRUNCATE's DropRelationBuffers takes the buffer content lock, waits out the
// I/O, and DISCARDS the dirty page without writing.
//
// goopg's parity mechanism is the slot generation: InvalidateRel bumps the
// generation of an ioInflight slot, and evictVictim re-checks it (pre-write
// and post-flush-failure) and discards the flush on mismatch.
//
// The test drives the interleaving deterministically through OnFlushWait: the
// victim's flush is parked inside evictVictim (contentMu held, before the
// gen check/flushSlot) while the TRUNCATE side runs InvalidateRel +
// TruncateRelation. Before the fix, pinNew returns "flush victim: write …:
// out of range (nblocks=0)"; with the fix, the stale flush is discarded, the
// pin succeeds, and the page was never written.
func TestTruncateDuringEvictionFlushDiscardsStalePage(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	pool, err := NewPool(mgr, PoolConfig{Slots: 2})
	if err != nil {
		t.Fatal(err)
	}
	relA := RelFileNode{DBOid: 1, RelOid: 7401, Fork: MainFork}
	relB := RelFileNode{DBOid: 1, RelOid: 7402, Fork: MainFork}

	// Put one dirty page of A in each slot (both slots are then occupied by
	// A's blocks 0 and 1, matching the nightly shape where the history
	// relation's dirty pages are the clock-sweep victims).
	for blk := BlockNumber(0); blk < 2; blk++ {
		s, n, err := pool.PinNew(relA)
		if err != nil {
			t.Fatal(err)
		}
		if n != blk {
			t.Fatalf("PinNew gave block %d, want %d", n, blk)
		}
		pool.MarkDirty(s)
		pool.Unpin(s)
	}

	var (
		once sync.Once
	)
	// Park the eviction flush, then run the TRUNCATE side (statement order:
	// InvalidateRel first, smgr truncate second — operators_ddl
	// truncateTableAndPartitions) while the flush is in flight.
	pool.OnFlushWait = func() {
		once.Do(func() {
			pool.InvalidateRel(relA)
			if err := mgr.TruncateRelation(relA); err != nil {
				t.Errorf("TruncateRelation: %v", err)
			}
		})
	}

	// Demand a slot: pinNew of a fresh relation must evict an A page.
	slotB, blkB, err := pool.PinNew(relB)
	pool.OnFlushWait = nil
	if err != nil {
		t.Fatalf("PinNew(relB) failed: %v (regression of AI-20261031-002109-005: "+
			"the eviction flush of the concurrently-truncated relation was not discarded)", err)
	}
	if blkB != 0 {
		t.Fatalf("PinNew(relB) gave block %d, want 0", blkB)
	}
	pool.Unpin(slotB)

	// The stale page must not have been written: A's file is 0 bytes.
	st, serr := os.Stat(filepath.Join(dir, "base", "1", "7401"))
	if serr != nil {
		t.Fatalf("stat relA: %v", serr)
	}
	if st.Size() != 0 {
		t.Fatalf("relA file size %d, want 0: the discarded page was written", st.Size())
	}
	if n, err := pool.NBlocks(relA); err != nil || n != 0 {
		t.Fatalf("relA nblocks %d err %v, want 0", n, err)
	}
	// The invalidated tags are gone from the bufmap.
	for blk := BlockNumber(0); blk < 2; blk++ {
		idx, _ := pool.bm.Lookup(BufferTag{Rel: relA, Block: blk})
		if idx >= 0 {
			t.Fatalf("bufmap still maps relA block %d to slot %d after discard", blk, idx)
		}
	}
}

// TestInvalidateRelBumpsGenerationOfClaimedSlot pins the signaling half of
// the mechanism: an ioInflight (claimed) slot of the invalidated relation
// gets a generation bump so the in-flight evictVictim discards, since the
// valid/dirty bit-clear cannot reach it (the claim already cleared them).
func TestInvalidateRelBumpsGenerationOfClaimedSlot(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	pool, err := NewPool(mgr, PoolConfig{Slots: 2})
	if err != nil {
		t.Fatal(err)
	}
	relA := RelFileNode{DBOid: 1, RelOid: 7411, Fork: MainFork}

	s, _, err := pool.PinNew(relA)
	if err != nil {
		t.Fatal(err)
	}
	pool.MarkDirty(s)
	pool.Unpin(s)

	pool.pinMu.Lock()
	var (
		idx, claimGen uint32
		found         bool
	)
	// The first probe passes the clock-sweep bookkeeping (free/usage slots)
	// and claims a free slot; the dirty A page is claimed once its usage
	// count is consumed.
	for k := 0; k < 8; k++ {
		i, wd, tg, g, e := pool.claimVictim()
		if e == ErrNoBuffer {
			break
		}
		if e != nil {
			pool.pinMu.Unlock()
			t.Fatal(e)
		}
		if wd && tg.Rel == relA {
			idx, claimGen, found = uint32(i), g, true
			break
		}
	}
	pool.pinMu.Unlock()
	if !found {
		t.Fatal("claimVictim never produced a dirty relA claim")
	}

	pool.InvalidateRel(relA)

	st := pool.slots[idx].state.Load()
	if stateGen(st) == claimGen {
		t.Fatalf("generation of claimed slot not bumped by InvalidateRel (gen=%d): "+
			"the in-flight flush would not discard", claimGen)
	}
	if !stateIO(st) {
		t.Fatalf("InvalidateRel disturbed the ioInflight bit of the claimed slot: state=%#016x", st)
	}
}

// TestFlushBatchSkipsInvalidatedSlots covers the checkpoint path: a slot
// whose dirty/valid bits were cleared by a concurrent TRUNCATE/DROP
// invalidate must not be written by an in-flight checkpoint batch (PG's
// checkpoint never writes buffers DropRelationBuffers discarded, and a
// stale block write past a concurrent truncate's new end fails in smgr).
func TestFlushBatchSkipsInvalidatedSlots(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	pool, err := NewPool(mgr, PoolConfig{Slots: 4})
	if err != nil {
		t.Fatal(err)
	}
	relA := RelFileNode{DBOid: 1, RelOid: 7421, Fork: MainFork}

	s, _, err := pool.PinNew(relA)
	if err != nil {
		t.Fatal(err)
	}
	pool.MarkDirty(s)
	pool.Unpin(s)
	tag := BufferTag{Rel: relA, Block: 0}
	before := pool.sharedCheckpointWrittenCount.Load()

	// TRUNCATE side: invalidate first, smgr truncate second.
	pool.InvalidateRel(relA)
	if err := mgr.TruncateRelation(relA); err != nil {
		t.Fatal(err)
	}

	// The checkpoint batch was assembled from the pre-invalidate dirty scan.
	if err := pool.flushBatch([]*Slot{s}, []BufferTag{tag}); err != nil {
		t.Fatalf("flushBatch wrote an invalidated slot: %v", err)
	}
	if got := pool.sharedCheckpointWrittenCount.Load(); got != before {
		t.Fatalf("flushBatch counted an invalidated slot as written: %d → %d", before, got)
	}
	st := s.state.Load()
	if stateDirty(st) {
		t.Fatalf("invalidated slot regained dirty after flushBatch: state=%#016x", st)
	}
}
