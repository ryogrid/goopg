package vacuum

import (
	"testing"
	"time"

	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/storage"
)

// cleanupLockFixture builds one page holding a live tuple and a tuple dead to
// every snapshot, and returns the dead tuple's slot.
func cleanupLockFixture(t *testing.T, pool *storage.Pool, rel storage.RelFileNode) (*transam.Manager, uint16) {
	t.Helper()
	s, _, err := pool.PinNew(rel)
	if err != nil {
		t.Fatal(err)
	}
	pool.Unpin(s)
	mvccMgr := transam.NewManager()
	var xids [2]storage.TransactionID
	for i := range xids {
		tx, _ := mvccMgr.Begin(transam.IsolationReadCommitted)
		xid, _ := mvccMgr.AssignXID(tx)
		tx.XID = xid
		mvccMgr.Commit(tx)
		xids[i] = xid
	}
	addTuple(t, pool, rel, 0, storage.NewHeapTuple(xids[0], storage.InvalidTransactionID, []byte("alive")))
	dead := addTuple(t, pool, rel, 0, storage.NewHeapTuple(xids[0], xids[1], []byte("dead-tuple-data")))
	return mvccMgr, dead
}

// TestVacuumPinnedPageNonAggressiveCountsWithoutPruning pins
// lazy_scan_noprune: a non-aggressive pass that cannot get the cleanup lock
// (another backend holds a pin) does not prune the page, still counts its
// live tuple for reltuples, and blocks relfrozenxid advancement (M0145-0008q).
func TestVacuumPinnedPageNonAggressiveCountsWithoutPruning(t *testing.T) {
	pool, _, rel, cleanup := newRel(t)
	defer cleanup()
	mvccMgr, deadSlot := cleanupLockFixture(t, pool, rel)

	hold, err := pool.Pin(storage.BufferTag{Rel: rel, Block: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Unpin(hold)

	stats, err := VacuumWithOptions(pool, mvccMgr, rel, VacuumOptions{})
	if err != nil {
		t.Fatalf("VacuumWithOptions: %v", err)
	}
	if stats.Dead != 0 || stats.Live != 1 || stats.SkippedPinned != 1 {
		t.Fatalf("stats=%+v want Dead=0 Live=1 SkippedPinned=1", stats)
	}
	if !stats.RelfrozenxidGuarded(false) {
		t.Fatal("a pinned-page skip must guard relfrozenxid on a non-aggressive pass")
	}
	if _, err := storage.PageGetHeapTuple(hold.Page(), deadSlot); err != nil {
		t.Fatalf("dead tuple was reclaimed under a foreign pin: %v", err)
	}
}

// TestVacuumPinnedPageAggressiveWaitsForCleanupLock pins
// LockBufferForCleanup: an aggressive pass waits until the other pin is
// dropped, then prunes the page.
func TestVacuumPinnedPageAggressiveWaitsForCleanupLock(t *testing.T) {
	pool, _, rel, cleanup := newRel(t)
	defer cleanup()
	mvccMgr, _ := cleanupLockFixture(t, pool, rel)

	hold, err := pool.Pin(storage.BufferTag{Rel: rel, Block: 0})
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(30 * time.Millisecond)
		close(released)
		pool.Unpin(hold)
	}()

	stats, err := VacuumWithOptions(pool, mvccMgr, rel, VacuumOptions{Aggressive: true})
	if err != nil {
		t.Fatalf("VacuumWithOptions: %v", err)
	}
	select {
	case <-released:
	default:
		t.Fatal("aggressive vacuum pruned before the foreign pin was dropped")
	}
	if stats.Dead != 1 || stats.Live != 1 || stats.SkippedPinned != 0 {
		t.Fatalf("stats=%+v want Dead=1 Live=1 SkippedPinned=0", stats)
	}
}

// TestVacuumCollectsPreexistingLPDeadItems pins lazy_scan_prune's
// deadoffsets (M0145-0008v): a tuple an on-access prune already left LP_DEAD
// still has an index entry, so VACUUM must hand its TID to index cleanup even
// though its own prune finds nothing left to do on the page.
func TestVacuumCollectsPreexistingLPDeadItems(t *testing.T) {
	pool, _, rel, cleanup := newRel(t)
	defer cleanup()
	mvccMgr, deadSlot := cleanupLockFixture(t, pool, rel)

	s, err := pool.Pin(storage.BufferTag{Rel: rel, Block: 0})
	if err != nil {
		t.Fatal(err)
	}
	s.Lock()
	h := storage.MustHeader(s.Page())
	h.SetPruneXID(1)
	r, err := storage.PagePruneOpt(s.Page(), mvccMgr.OldestXmin())
	s.Unlock()
	pool.Unpin(s)
	if err != nil || len(r.Dead) != 1 || r.Dead[0] != deadSlot {
		t.Fatalf("on-access prune = %+v, %v; want Dead=[%d]", r, err, deadSlot)
	}

	stats, err := VacuumWithOptions(pool, mvccMgr, rel, VacuumOptions{})
	if err != nil {
		t.Fatalf("VacuumWithOptions: %v", err)
	}
	want := storage.ItemPointer{Block: 0, Offset: deadSlot}
	if len(stats.DeadTIDs) != 1 || stats.DeadTIDs[0] != want {
		t.Fatalf("DeadTIDs = %v, want [%v]", stats.DeadTIDs, want)
	}
}
