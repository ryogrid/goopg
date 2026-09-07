package storage

import (
	"errors"
	"testing"
)

// E-19 slice S2 — the accounting surface, and the seam that lets it be tested.
//
// §4.2 of docs/design/storage-prefetch-buffer/E19-INSTALLING-PREFETCH.md names
// two hazards no values suite can ever see, because neither produces a wrong
// answer: a prefetch that pins and never unpins (the buffer becomes
// permanently un-evictable), and a prefetch that evicts a live buffer. Both
// are assertions about POOL STATE, and before S2 neither was reachable from a
// test: getPinCount is unexported, SlotPinCount needs a tag the test would
// have to guess, and there was no way to make a read fail without swapping the
// Manager or truncating the file underneath it — either of which changes the
// thing under test.

func prefetchTestPool(t *testing.T, slots int) (*Manager, *Pool, RelFileNode) {
	t.Helper()
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir, ChecksumsEnabled: true})
	t.Cleanup(func() { mgr.Close() })
	pool, err := NewPool(mgr, PoolConfig{Slots: slots})
	if err != nil {
		t.Fatal(err)
	}
	rel := RelFileNode{DBOid: 1, RelOid: 900, Fork: MainFork}
	return mgr, pool, rel
}

// TestTotalPinCountTracksPinUnpin is the baseline the leak arms are measured
// against: on a quiesced pool the sum is exact, and it returns to its starting
// value. Without this the leak test has no instrument.
func TestTotalPinCountTracksPinUnpin(t *testing.T) {
	mgr, pool, rel := prefetchTestPool(t, 16)
	const n = 5
	extendN(t, mgr, rel, n)

	base, baseSlots := pool.TotalPinCount()
	if base != 0 || baseSlots != 0 {
		t.Fatalf("fresh pool has %d pins over %d slots, want 0/0", base, baseSlots)
	}

	slots := make([]*Slot, n)
	for i := 0; i < n; i++ {
		s, err := pool.Pin(BufferTag{Rel: rel, Block: BlockNumber(i)})
		if err != nil {
			t.Fatalf("Pin %d: %v", i, err)
		}
		slots[i] = s
		total, pinned := pool.TotalPinCount()
		if total != int64(i+1) || pinned != i+1 {
			t.Fatalf("after %d pins: total=%d slots=%d, want %d/%d", i+1, total, pinned, i+1, i+1)
		}
	}

	// A second pin of an already-pinned tag must show up in the SUM but not in
	// the slot count — the distinction the leak test needs to tell "one slot
	// pinned twice" from "two slots leaked".
	extra, err := pool.Pin(BufferTag{Rel: rel, Block: 0})
	if err != nil {
		t.Fatal(err)
	}
	if total, pinned := pool.TotalPinCount(); total != n+1 || pinned != n {
		t.Fatalf("double pin: total=%d slots=%d, want %d/%d", total, pinned, n+1, n)
	}
	pool.Unpin(extra)

	for _, s := range slots {
		pool.Unpin(s)
	}
	if total, pinned := pool.TotalPinCount(); total != 0 || pinned != 0 {
		t.Fatalf("after balanced unpins: total=%d slots=%d, want 0/0", total, pinned)
	}
}

var errInjectedRead = errors.New("e19: injected read fault")

// TestDebugReadFaultMakesPinFailCleanly is the seam's own gate AND a real
// assertion about HEAD: a read that fails must leave the pool exactly as it
// found it — no pin, no slot stuck with the IO bit set, and no bufmap entry
// pointing at a slot that never got its page. §4.2's third leak arm (the
// injected read error) is unwritable without it, and the same failure shape is
// the one S3's StartRead has to reproduce on its own error path.
func TestDebugReadFaultMakesPinFailCleanly(t *testing.T) {
	mgr, pool, rel := prefetchTestPool(t, 16)
	extendN(t, mgr, rel, 3)

	tag := BufferTag{Rel: rel, Block: 1}
	pool.DebugReadFault = func(got BufferTag) error {
		if got == tag {
			return errInjectedRead
		}
		return nil
	}

	if _, err := pool.Pin(tag); !errors.Is(err, errInjectedRead) {
		t.Fatalf("Pin with injected fault: err = %v, want %v", err, errInjectedRead)
	}
	if total, pinned := pool.TotalPinCount(); total != 0 || pinned != 0 {
		t.Fatalf("failed read leaked pins: total=%d slots=%d, want 0/0", total, pinned)
	}
	for i := range pool.slots {
		if st := pool.slots[i].state.Load(); stateIO(st) {
			t.Fatalf("slot %d still has the IO bit set after a failed read (state=%#x)", i, st)
		}
	}
	if idx, _ := pool.bm.Lookup(tag); idx >= 0 {
		t.Fatalf("failed read left tag %v mapped to slot %d", tag, idx)
	}

	// Clearing the fault must make the very same tag readable — i.e. the
	// failure was not sticky and did not poison the slot.
	pool.DebugReadFault = nil
	s, err := pool.Pin(tag)
	if err != nil {
		t.Fatalf("Pin after clearing the fault: %v", err)
	}
	pool.Unpin(s)
	if total, _ := pool.TotalPinCount(); total != 0 {
		t.Fatalf("post-recovery pin sum = %d, want 0", total)
	}
}

// TestEvictionCountIsAWorkingInstrument gates the OTHER half of §4.2: hazard 2
// is "a window of depth D never raises the eviction count above the same scan
// at D=0 by more than D", which is only meaningful if the counter actually
// moves when buffers are evicted and does not move when they are not.
func TestEvictionCountIsAWorkingInstrument(t *testing.T) {
	mgr, pool, rel := prefetchTestPool(t, 4)
	const n = 12
	extendN(t, mgr, rel, n)

	// Touch four distinct blocks in a four-slot pool: fills it, evicts nothing
	// (every victim slot starts free, so evictVictim's oldTag is empty).
	for i := 0; i < 4; i++ {
		s, err := pool.Pin(BufferTag{Rel: rel, Block: BlockNumber(i)})
		if err != nil {
			t.Fatal(err)
		}
		pool.Unpin(s)
	}
	afterFill := pool.EvictionCount()
	if afterFill != 0 {
		t.Fatalf("filling a cold pool counted %d evictions, want 0", afterFill)
	}

	// Now walk past the pool's capacity: every miss must displace somebody.
	for i := 4; i < n; i++ {
		s, err := pool.Pin(BufferTag{Rel: rel, Block: BlockNumber(i)})
		if err != nil {
			t.Fatal(err)
		}
		pool.Unpin(s)
	}
	if got := pool.EvictionCount(); got == 0 {
		t.Fatalf("walked %d blocks through a %d-slot pool and counted 0 evictions", n, 4)
	}
	if total, _ := pool.TotalPinCount(); total != 0 {
		t.Fatalf("eviction walk leaked %d pins", total)
	}
}
