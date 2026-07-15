package vacuum

import (
	"testing"

	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/storage"
)

func newRel(t *testing.T) (*storage.Pool, *storage.Manager, storage.RelFileNode, func()) {
	t.Helper()
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 8})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	rel := storage.RelFileNode{DBOid: 1, RelOid: 7000, Fork: storage.MainFork}
	cleanup := func() {
		_ = pool.Close()
		_ = mgr.Close()
	}
	return pool, mgr, rel, cleanup
}

// addTuple writes a tuple into the (currently single) page of rel and
// returns its line-pointer slot.
func addTuple(t *testing.T, pool *storage.Pool, rel storage.RelFileNode, blk storage.BlockNumber, tuple storage.HeapTuple) uint16 {
	t.Helper()
	slot, err := pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
	if err != nil {
		t.Fatalf("pin: %v", err)
	}
	defer pool.Unpin(slot)
	s, err := storage.PageAddHeapTuple(slot.Page(), tuple)
	if err != nil {
		t.Fatalf("PageAddHeapTuple: %v", err)
	}
	pool.MarkDirty(slot)
	return s
}

// TestVacuumReclaimsDeadTuples pins the contract: a tuple with xmax
// committed below the oldest-xmin horizon transitions to LP_UNUSED,
// freeing space; live tuples remain accessible at their original slot.
func TestVacuumReclaimsDeadTuples(t *testing.T) {
	pool, _, rel, cleanup := newRel(t)
	defer cleanup()

	// Allocate the first block.
	s, _, err := pool.PinNew(rel)
	if err != nil {
		t.Fatal(err)
	}
	pool.Unpin(s)

	mvccMgr := mvcc.NewManager()
	// Drive next-xid forward so we can place tuples with xids
	// that will lie below the eventual horizon. M0093: AssignXID
	// to actually materialise the XIDs we then stamp into tuples.
	tx1, _ := mvccMgr.Begin(mvcc.IsolationReadCommitted)
	xid1, _ := mvccMgr.AssignXID(tx1)
	tx1.XID = xid1
	mvccMgr.Commit(tx1)
	tx2, _ := mvccMgr.Begin(mvcc.IsolationReadCommitted)
	xid2, _ := mvccMgr.AssignXID(tx2)
	tx2.XID = xid2
	mvccMgr.Commit(tx2)

	// Tuple A: live (xmin=tx1, xmax=0).
	live := storage.NewHeapTuple(tx1.XID, storage.InvalidTransactionID, []byte("alive"))
	liveSlot := addTuple(t, pool, rel, 0, live)

	// Tuple B: dead (xmin=tx1, xmax=tx2 — both committed and below horizon).
	dead := storage.NewHeapTuple(tx1.XID, tx2.XID, []byte("dead-tuple-data"))
	deadSlot := addTuple(t, pool, rel, 0, dead)

	stats, err := Vacuum(pool, mvccMgr, rel)
	if err != nil {
		t.Fatalf("Vacuum: %v", err)
	}
	if stats.Dead != 1 || stats.Live != 1 {
		t.Fatalf("stats=%+v want Dead=1 Live=1", stats)
	}

	// Live slot still resolves and reads back the same data.
	bs, err := pool.Pin(storage.BufferTag{Rel: rel, Block: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Unpin(bs)
	got, err := storage.PageGetHeapTuple(bs.Page(), liveSlot)
	if err != nil {
		t.Fatalf("PageGetHeapTuple(live=%d): %v", liveSlot, err)
	}
	if string(got.Data) != "alive" {
		t.Errorf("live tuple data=%q want=%q", got.Data, "alive")
	}

	// Dead slot is now LP_UNUSED and surfaces as ErrUnsupportedItem.
	if _, err := storage.PageGetHeapTuple(bs.Page(), deadSlot); err == nil {
		t.Errorf("expected error reading reclaimed slot %d", deadSlot)
	}
}

// TestVacuumKeepsLiveBelowOldestXmin verifies that an in-progress
// transaction's xid keeps a tuple's xmax above the horizon, so the
// tuple is NOT reclaimed even though xmax is committed-from-the-pov
// of newer snapshots.
func TestVacuumKeepsLiveBelowOldestXmin(t *testing.T) {
	pool, _, rel, cleanup := newRel(t)
	defer cleanup()

	s, _, err := pool.PinNew(rel)
	if err != nil {
		t.Fatal(err)
	}
	pool.Unpin(s)

	mvccMgr := mvcc.NewManager()
	tx1, _ := mvccMgr.Begin(mvcc.IsolationReadCommitted)
	mvccMgr.Commit(tx1)

	// Open a long-running transaction that pins the horizon at its xid.
	holder, _ := mvccMgr.Begin(mvcc.IsolationRepeatableRead)
	defer mvccMgr.Rollback(holder)

	// A later tx commits a delete (xmax=delTx.XID).
	delTx, _ := mvccMgr.Begin(mvcc.IsolationReadCommitted)
	mvccMgr.Commit(delTx)

	tup := storage.NewHeapTuple(tx1.XID, delTx.XID, []byte("still-needed"))
	tupSlot := addTuple(t, pool, rel, 0, tup)

	// holder.XID < delTx.XID, so OldestXmin == holder.XID, and
	// delTx.XID is NOT < horizon. Tuple stays.
	stats, err := Vacuum(pool, mvccMgr, rel)
	if err != nil {
		t.Fatalf("Vacuum: %v", err)
	}
	if stats.Dead != 0 || stats.Live != 1 {
		t.Fatalf("stats=%+v want Dead=0 Live=1", stats)
	}

	bs, err := pool.Pin(storage.BufferTag{Rel: rel, Block: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Unpin(bs)
	if _, err := storage.PageGetHeapTuple(bs.Page(), tupSlot); err != nil {
		t.Errorf("expected tuple still readable, got err=%v", err)
	}
}

// TestVacuumReclaimsFreeSpace: after vacuum, pd_upper should rise back
// up so subsequent inserts find room in the previously-occupied bytes.
func TestVacuumReclaimsFreeSpace(t *testing.T) {
	pool, _, rel, cleanup := newRel(t)
	defer cleanup()

	s, _, err := pool.PinNew(rel)
	if err != nil {
		t.Fatal(err)
	}
	pool.Unpin(s)

	mvccMgr := mvcc.NewManager()
	// M0093: AssignXID so the tuples are stamped with real XIDs.
	creator, _ := mvccMgr.Begin(mvcc.IsolationReadCommitted)
	xidC, _ := mvccMgr.AssignXID(creator)
	creator.XID = xidC
	mvccMgr.Commit(creator)
	deleter, _ := mvccMgr.Begin(mvcc.IsolationReadCommitted)
	xidD, _ := mvccMgr.AssignXID(deleter)
	deleter.XID = xidD
	mvccMgr.Commit(deleter)

	bigPayload := make([]byte, 1000)
	for i := range bigPayload {
		bigPayload[i] = 'x'
	}
	for i := 0; i < 5; i++ {
		// Make a payload distinct enough to be located later.
		body := append([]byte(nil), bigPayload...)
		body[0] = byte(i)
		tup := storage.NewHeapTuple(creator.XID, deleter.XID, body)
		addTuple(t, pool, rel, 0, tup)
	}

	bs, err := pool.Pin(storage.BufferTag{Rel: rel, Block: 0})
	if err != nil {
		t.Fatal(err)
	}
	hBefore := storage.MustHeader(bs.Page())
	upperBefore := hBefore.Upper()
	pool.Unpin(bs)

	stats, err := Vacuum(pool, mvccMgr, rel)
	if err != nil {
		t.Fatalf("Vacuum: %v", err)
	}
	if stats.Dead != 5 {
		t.Fatalf("Dead=%d want=5", stats.Dead)
	}

	bs, err = pool.Pin(storage.BufferTag{Rel: rel, Block: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Unpin(bs)
	hAfter := storage.MustHeader(bs.Page())
	if hAfter.Upper() <= upperBefore {
		t.Errorf("pd_upper not reclaimed: before=%d after=%d", upperBefore, hAfter.Upper())
	}
	// Free space window must equal the full page minus the line-pointer
	// area (5 LP_UNUSED slots stay around) and the page header.
	want := int(hAfter.Special()) - (storage.SizeOfPageHeaderData + 5*4)
	if hAfter.FreeSpace() != want {
		t.Errorf("FreeSpace=%d want=%d", hAfter.FreeSpace(), want)
	}
}

// TestAnalyzeReturnsRowCountAndAvgWidth: ANALYZE walks every page and
// returns reltuples + average width over visible rows.
func TestAnalyzeReturnsRowCountAndAvgWidth(t *testing.T) {
	pool, _, rel, cleanup := newRel(t)
	defer cleanup()

	s, _, err := pool.PinNew(rel)
	if err != nil {
		t.Fatal(err)
	}
	pool.Unpin(s)

	mvccMgr := mvcc.NewManager()
	creator, _ := mvccMgr.Begin(mvcc.IsolationReadCommitted)
	xidC, _ := mvccMgr.AssignXID(creator)
	creator.XID = xidC
	mvccMgr.Commit(creator)

	addTuple(t, pool, rel, 0, storage.NewHeapTuple(creator.XID, storage.InvalidTransactionID, []byte("ab")))
	addTuple(t, pool, rel, 0, storage.NewHeapTuple(creator.XID, storage.InvalidTransactionID, []byte("hello")))

	stats, err := Analyze(pool, mvccMgr, rel, nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if stats.Rows != 2 {
		t.Errorf("Rows=%d want=2", stats.Rows)
	}
	if stats.Pages != 1 {
		t.Errorf("Pages=%d want=1", stats.Pages)
	}
	// Average width: both tuples have Hoff=24, data lengths 2 and 5, so
	// (24+2 + 24+5) / 2 = 27.5
	if stats.AvgWidth != 27.5 {
		t.Errorf("AvgWidth=%v want=27.5", stats.AvgWidth)
	}
}

// TestVacuumWithOptionsEmitsCanonicalPruneRecord guards M0119-0005's fix:
// before this, VacuumWithOptions emitted only goopg's native
// RecordKindHeapPruneOpt for a page with reclaimed tuples, so pg_waldump
// (or a real PG18 standby) saw zero WAL activity for a page whose only
// change was VACUUM pruning. VacuumOptions.LogCanonical must now receive one
// PG-canonical XLOG_HEAP2_PRUNE_VACUUM_SCAN payload per pruned page.
func TestVacuumWithOptionsEmitsCanonicalPruneRecord(t *testing.T) {
	t.Skip("canonical WAL emission removed 2026-07-15 (native\u2192PG (rmid,info) dispatch); intentional, not a regression \u2014 see docs/design/wal-native-pg-format/04 + .ralph/deferral_ledger.md")
}

// TestVacuumWithOptionsNilLogCanonicalIsNoop verifies the default
// (LogCanonical unset) VacuumOptions zero value keeps VacuumWithOptions's
// pre-M0119-0005 behaviour unchanged — no panic, no extra WAL.
// TestVacuumWithOptionsDefaultReclaims verifies the default VacuumOptions
// zero value reclaims dead tuples as expected.
func TestVacuumWithOptionsDefaultReclaims(t *testing.T) {
	pool, _, rel, cleanup := newRel(t)
	defer cleanup()

	s, _, err := pool.PinNew(rel)
	if err != nil {
		t.Fatal(err)
	}
	pool.Unpin(s)

	mvccMgr := mvcc.NewManager()
	tx1, _ := mvccMgr.Begin(mvcc.IsolationReadCommitted)
	xid1, _ := mvccMgr.AssignXID(tx1)
	tx1.XID = xid1
	mvccMgr.Commit(tx1)
	tx2, _ := mvccMgr.Begin(mvcc.IsolationReadCommitted)
	xid2, _ := mvccMgr.AssignXID(tx2)
	tx2.XID = xid2
	mvccMgr.Commit(tx2)

	dead := storage.NewHeapTuple(tx1.XID, tx2.XID, []byte("dead-tuple-data"))
	addTuple(t, pool, rel, 0, dead)

	stats, err := VacuumWithOptions(pool, mvccMgr, rel, VacuumOptions{})
	if err != nil {
		t.Fatalf("VacuumWithOptions: %v", err)
	}
	if stats.Dead != 1 {
		t.Fatalf("stats.Dead=%d want=1", stats.Dead)
	}
}
