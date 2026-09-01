package storage

import (
	"os"
	"testing"
)

// TestMain installs a permissive XidCommitted hook for the storage unit
// suite: these tests construct synthetic tuples with literal xids and no
// CLOG, and historically assumed "xmax < horizon => dead" (the pre-C3-S3
// predicate). Production wires the hook to CLog.DidCommit in initdb.Open;
// tests that need abort semantics override the hook locally (save/restore).
func TestMain(m *testing.M) {
	XidCommitted = func(TransactionID) bool { return true }
	os.Exit(m.Run())
}

// TestTupleDeadToAllAbortedDeleter pins the C3-S3 blocker fix B: a deleter
// below the horizon whose xid did NOT commit (rolled back) must not make
// the tuple dead — reclaiming it would destroy a live row (the aborted
// DELETE's stamp survives physically; PG checks TransactionIdDidCommit in
// HeapTupleSatisfiesVacuum).
func TestTupleDeadToAllAbortedDeleter(t *testing.T) {
	saved := XidCommitted
	defer func() { XidCommitted = saved }()

	hdr := NewHeapTuple(1, 5, []byte("x")).Header // xmax 5 < horizon 10

	XidCommitted = func(xid TransactionID) bool { return xid != 5 } // 5 aborted
	if TupleDeadToAll(hdr, 10) {
		t.Fatal("tuple with ABORTED deleter treated as dead-to-all")
	}
	XidCommitted = func(TransactionID) bool { return true }
	if !TupleDeadToAll(hdr, 10) {
		t.Fatal("tuple with committed deleter below horizon must be dead")
	}
	XidCommitted = nil // no hook (bootstrap/unit default): conservative
	if TupleDeadToAll(hdr, 10) {
		t.Fatal("nil hook must be conservative (not dead)")
	}
}

// TestPagePruneOptAbortedDeleterKeepsRow: end-to-end prune must keep the
// row when the deleter aborted.
func TestPagePruneOptAbortedDeleterKeepsRow(t *testing.T) {
	saved := XidCommitted
	defer func() { XidCommitted = saved }()
	XidCommitted = func(xid TransactionID) bool { return xid != 5 }

	page := make(Page, BlockSize)
	if err := InitPage(page); err != nil {
		t.Fatal(err)
	}
	tup := NewHeapTuple(1, 5, []byte("survivor"))
	slot, err := PageAddHeapTuple(page, tup)
	if err != nil {
		t.Fatal(err)
	}
	MustHeader(page).SetPruneXID(5)
	if _, err := PagePruneOpt(page, 10); err != nil {
		t.Fatal(err)
	}
	got, err := PageGetHeapTuple(page, slot)
	if err != nil {
		t.Fatalf("row pruned despite ABORTED deleter: %v", err)
	}
	if string(got.Data) != "survivor" {
		t.Fatalf("row corrupted: %q", got.Data)
	}
}

// TestTupleDeadToAllSubxactDeleter (C3-S3 design case c): a tuple deleted
// by a SUBTRANSACTION is dead-to-all only when the sub-xid's commit fate
// resolves committed through its parent. NOTE (S3 review MUST-FIX 2):
// production does NOT yet stamp sub-commit lanes into the CLOG
// (SetSubCommitted has no production caller), so CLog.DidCommit returns
// false for sub-xids — CONSERVATIVE: subxact-deleted tuples stay
// unreclaimable by prune/VACUUM/the kill oracle until CLOG truncation
// (vacuum-bloat, not corruption). This test pins the PREDICATE's parent
// semantics with a synthetic hook; the production stamping gap
// (TransactionIdCommitTree parity) is a deferral-ledger item.
func TestTupleDeadToAllSubxactDeleter(t *testing.T) {
	saved := XidCommitted
	defer func() { XidCommitted = saved }()

	hdr := NewHeapTuple(1, 7, []byte("x")).Header // deleter = sub-xid 7

	parentCommitted := true
	XidCommitted = func(xid TransactionID) bool {
		if xid == 7 { // sub-commit resolves via parent 8
			return parentCommitted
		}
		return false
	}
	if !TupleDeadToAll(hdr, 10) {
		t.Fatal("subxact deleter with committed parent: want dead-to-all")
	}
	parentCommitted = false // parent rolled back
	if TupleDeadToAll(hdr, 10) {
		t.Fatal("subxact deleter with aborted parent treated as dead")
	}
}

// walFlushRecorder records the highest FlushUpTo target — the hint
// flush-barrier oracle.
type walFlushRecorder struct{ maxFlushed uint64 }

func (w *walFlushRecorder) FlushUpTo(lsn uint64) error {
	if lsn > w.maxFlushed {
		w.maxFlushed = lsn
	}
	return nil
}

func (w *walFlushRecorder) WalRecords() int64 { return 0 }
func (w *walFlushRecorder) WalBytes() int64   { return 0 }

// TestMarkDirtyHintFlushBarrier pins the C3-S3 async-commit fix: a page
// dirtied ONLY by an unlogged hint (pd_lsn untouched) must still force a
// WAL flush up to the frontier observed at mark time before the page
// reaches disk — otherwise a crash could persist an LP_DEAD mark whose
// premise (the deleter's commit record) was lost.
func TestMarkDirtyHintFlushBarrier(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dir})
	defer mgr.Close()

	rec := &walFlushRecorder{}
	frontier := uint64(0)
	pool, err := NewPool(mgr, PoolConfig{
		Slots:       4,
		WAL:         rec,
		WALFrontier: func() uint64 { return frontier },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	rel := RelFileNode{DBOid: 1, RelOid: 604, Fork: MainFork}
	s, _, err := pool.PinNew(rel)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate: the oracle observed a commit whose record sits at WAL
	// position 12345 (appended, unflushed). Hint-mark the page.
	frontier = 12345
	s.Lock()
	pool.MarkDirtyHint(s)
	s.Unlock()
	pool.Unpin(s)

	// Any write-back path must flush WAL >= the mark-time frontier even
	// though pd_lsn never advanced.
	if err := pool.FlushAll(); err != nil {
		t.Fatal(err)
	}
	if rec.maxFlushed < 12345 {
		t.Fatalf("page written with WAL flushed only to %d; hint barrier 12345 ignored (async-commit hole)", rec.maxFlushed)
	}
}

// TestTupleDeadToAllWraparound is the review/260831-2 ST-2 guard: the
// horizon test used a plain unsigned `effXmax >= oldestXmin`, so once the XID
// counter wrapped past 2^31 a deleter that is NEWER than the horizon but
// numerically smaller compared as "older" and its tuple was declared
// dead-to-all — VACUUM/prune would then reclaim a row that is still visible.
// PG compares with TransactionIdPrecedes (modular), which is XIDPrecedes here.
func TestTupleDeadToAllWraparound(t *testing.T) {
	// horizon just below the wrap point; deleter just past it, i.e. NEWER
	// than the horizon in modular order but numerically far smaller.
	const horizon = TransactionID(0xFFFFFF00)
	const deleter = TransactionID(50)
	if !XIDPrecedes(horizon, deleter) {
		t.Fatalf("test setup: %d must precede %d in modular order", horizon, deleter)
	}

	hdr := NewHeapTuple(TransactionID(0xFFFFFE00), deleter, []byte("x")).Header
	if TupleDeadToAll(hdr, horizon) {
		t.Fatal("deleter NEWER than the horizon (modular order) treated as dead-to-all")
	}

	// Sanity: a genuinely older deleter across the same wrap is still dead.
	hdrOld := NewHeapTuple(TransactionID(0xFFFFFD00), TransactionID(0xFFFFFE00), []byte("x")).Header
	if !TupleDeadToAll(hdrOld, horizon) {
		t.Fatal("deleter older than the horizon must be dead-to-all")
	}
}
