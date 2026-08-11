package storage

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestPinNewLosingExtenderDoesNotKeepDuplicateSlot is the regression gate for
// M0131-S30.3.
//
// PinNew releases pinMu across mgr.Extend. The instant Extend returns, the new
// block is inside nblocks, so a concurrent Pin misses the bufmap and loads it
// into a slot of its own. The extender then loses the bmInsert race — and
// before the fix, if the winner was still mid-read (ioInflight, so tryPinSlot
// refused it), the extender kept its own valid+dirty publication. That left TWO
// live slots for one block: the un-mapped one is a normal clock-sweep victim,
// so its near-empty image was later written back over whatever the mapped slot
// had accumulated, and every later HOT update on the resurrected page re-used
// line pointers the WAL had already spent.
//
// The test drives exactly that interleaving: the racing Pin is started inside
// OnExtendDone (the window where pinMu is not held) and parked inside its disk
// read via OnPinWait, so the extender reaches bmInsert while the winner's slot
// still has ioInflight set.
func TestPinNewLosingExtenderDoesNotKeepDuplicateSlot(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	pool, err := NewPool(mgr, PoolConfig{Slots: 16})
	if err != nil {
		t.Fatal(err)
	}
	rel := RelFileNode{DBOid: 1, RelOid: 7301, Fork: MainFork}

	// Block 0 first, so the raced extend produces the predictable block 1.
	first, blk0, err := pool.PinNew(rel)
	if err != nil {
		t.Fatal(err)
	}
	if blk0 != 0 {
		t.Fatalf("first PinNew gave block %d, want 0", blk0)
	}
	pool.MarkDirty(first)
	pool.Unpin(first)
	if err := pool.FlushAll(); err != nil {
		t.Fatal(err)
	}

	raced := BufferTag{Rel: rel, Block: 1}
	var (
		armed     atomic.Bool
		inIO      = make(chan struct{})
		releaseIO = make(chan struct{})
		once      sync.Once
		racerWG   sync.WaitGroup
		racer     *Slot
		racerErr  error
	)

	// Park the racing loader inside its read, holding ioInflight on the slot
	// it just published, until the extender has had time to lose bmInsert.
	pool.OnPinWait = func() {
		if !armed.Load() {
			return
		}
		once.Do(func() {
			close(inIO)
			<-releaseIO
		})
	}

	extendDone := false
	pool.OnExtendDone = func() {
		if extendDone {
			return // only the first extend is raced
		}
		extendDone = true
		armed.Store(true)
		racerWG.Add(1)
		go func() {
			defer racerWG.Done()
			racer, racerErr = pool.Pin(raced)
		}()
		<-inIO // the racer owns the bufmap entry and is stuck in its read
	}

	// Unpark the racer shortly after the extender re-acquires pinMu. It only
	// has to re-take the lock and fail one map insert, so this is generous;
	// the release must come from a third goroutine because the fixed extender
	// blocks on the racer's IO instead of falling through.
	go func() {
		<-inIO
		time.Sleep(300 * time.Millisecond)
		close(releaseIO)
	}()

	got, blk, err := pool.PinNew(rel)
	pool.OnExtendDone = nil
	racerWG.Wait()
	pool.OnPinWait = nil
	if err != nil {
		t.Fatalf("raced PinNew: %v", err)
	}
	if racerErr != nil {
		t.Fatalf("racing Pin(%v): %v", raced, racerErr)
	}
	if blk != 1 {
		t.Fatalf("raced PinNew gave block %d, want 1", blk)
	}
	if racer == nil {
		t.Fatal("the race window was never exercised")
	}
	if got != racer {
		t.Errorf("PinNew returned a different slot than the bufmap winner — the loser kept its own publication (M0131-S30.3)")
	}

	// The bufmap must point at exactly one slot, and no other slot may still
	// claim the tag: an un-mapped valid copy of a mapped block is the duplicate
	// that later overwrote real data.
	mappedIdx, _ := pool.bm.Lookup(raced)
	if mappedIdx < 0 {
		t.Fatalf("bufmap lost the tag %v entirely", raced)
	}
	for i := range pool.slots {
		if int32(i) == mappedIdx {
			continue
		}
		s := &pool.slots[i]
		if s.tag == raced && stateValid(s.state.Load()) {
			t.Errorf("slot %d is a duplicate live copy of %v (mapped slot is %d)", i, raced, mappedIdx)
		}
	}

	pool.MarkDirty(got)
	pool.Unpin(got)
	pool.Unpin(racer) // the racing Pin took a reference of its own
	if err := pool.FlushAll(); err != nil {
		t.Fatal(err)
	}
}
