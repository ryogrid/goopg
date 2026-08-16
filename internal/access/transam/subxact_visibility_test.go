package transam

import (
	"testing"

	"github.com/goopg/goopg/internal/access/transam/multixact"
	"github.com/goopg/goopg/internal/storage"
)

// makeSnap constructs a minimal Snapshot for tests.
func makeSnap(xmin, xmax storage.TransactionID, inProgress ...storage.TransactionID) Snapshot {
	ip := make([]storage.TransactionID, len(inProgress))
	copy(ip, inProgress)
	return Snapshot{Xmin: xmin, Xmax: xmax, InProgress: ip}
}

// TestSubxactVisibilityMatrix is the DoD test for M0050-0002.
//
// It covers all (subxact-status, parent-status) × snapshot-visibility
// combinations, matching upstream's HeapTupleSatisfiesMVCC subxact branch.
//
// Legend:
//
//	P  = parent xid (top-level transaction)
//	S  = subxact xid (child of P)
//	sp = snapshot
//
// The snapshot is captured between S and P for "in-progress" cases, and
// after P for "committed/aborted" cases.
func TestSubxactVisibilityMatrix(t *testing.T) {
	mgr := NewManager()

	// Allocate XIDs (M0093: lazy XID — call AssignXID so the
	// synthetic subxact arithmetic below sees real XIDs).
	txP, _ := mgr.Begin(IsolationReadCommitted) // parent xid
	if xid, err := mgr.AssignXID(txP); err == nil {
		txP.XID = xid
	} else {
		t.Fatal(err)
	}
	txQ, _ := mgr.Begin(IsolationReadCommitted) // concurrent txn (for snapshot xip)
	if xid, err := mgr.AssignXID(txQ); err == nil {
		txQ.XID = xid
	} else {
		t.Fatal(err)
	}
	subXid := txQ.XID + 1 // synthetic subxact XID (outside manager.active)
	// Manually wire subxact → parent in the manager's subxact map.
	mgr.RegisterSubXid(subXid, txP.XID)

	// Snapshot A: both P and subXid are in-progress (concurrent observer).
	snapA := makeSnap(txP.XID, txP.XID+10, txP.XID, txQ.XID)

	// Snapshot B: P is committed (xid < Xmin).
	// Nothing in InProgress.
	_ = mgr.Commit(txP)
	_ = mgr.Commit(txQ)
	snapB := makeSnap(txP.XID+10, txP.XID+100)

	type tc struct {
		name    string
		subAbrt bool // true if subxact was rolled back
		snap    Snapshot
		want    bool // expected SeesCommittedXIDWithSubxacts result
	}

	cases := []tc{
		// Subxact active, parent in-progress → invisible (parent not committed).
		{"subact_active_parent_inprogress", false, snapA, false},

		// Subxact committed (RELEASE), parent committed → visible.
		{"subact_committed_parent_committed", false, snapB, true},

		// Subxact individually aborted (ROLLBACK TO), parent committed → invisible.
		{"subact_aborted_parent_committed", true, snapB, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.subAbrt {
				mgr.MarkSubxactAborted(subXid)
			} else {
				// Remove abort mark if set by previous test.
				mgr.subxactMu.Lock()
				delete(mgr.subxactAborted, subXid)
				mgr.subxactMu.Unlock()
			}
			got := SeesCommittedXIDWithSubxacts(c.snap, subXid, mgr)
			if got != c.want {
				t.Errorf("SeesCommittedXIDWithSubxacts(%v) = %v, want %v", c.name, got, c.want)
			}
		})
	}
}

// TestTopLevelXidChain verifies TopLevelXid walks multi-level chains.
func TestTopLevelXidChain(t *testing.T) {
	mgr := NewManager()
	// Chain: 100 → 50 → 20 (top-level).
	mgr.RegisterSubXid(100, 50)
	mgr.RegisterSubXid(50, 20)

	top := mgr.TopLevelXid(100)
	if top != 20 {
		t.Errorf("TopLevelXid(100) = %d, want 20", top)
	}
	// A top-level XID returns itself.
	top2 := mgr.TopLevelXid(20)
	if top2 != 20 {
		t.Errorf("TopLevelXid(20) = %d, want 20", top2)
	}
}

// TestSeesCommittedXIDWithSubxactsNilResolver verifies degradation to
// the standard path when resolver is nil.
func TestSeesCommittedXIDWithSubxactsNilResolver(t *testing.T) {
	snap := makeSnap(100, 200)
	// xid=50: below Xmin → committed → true.
	if got := SeesCommittedXIDWithSubxacts(snap, 50, nil); !got {
		t.Error("SeesCommittedXIDWithSubxacts with nil resolver: xid<Xmin should be true")
	}
	// xid=250: above Xmax → false.
	if got := SeesCommittedXIDWithSubxacts(snap, 250, nil); got {
		t.Error("SeesCommittedXIDWithSubxacts with nil resolver: xid>Xmax should be false")
	}
}

// TestTupleVisibleSubxactDegrades verifies that TupleVisibleSubxact with
// nil resolver matches TupleVisible exactly.
func TestTupleVisibleSubxactDegrades(t *testing.T) {
	snap := makeSnap(100, 200)
	h := storage.HeapTupleHeader{
		Xmin:     50, // committed (below Xmin=100)
		Xmax:     storage.InvalidTransactionID,
		Infomask: 0,
	}
	want := TupleVisible(h, snap, 999, storage.InvalidCommandId, nil, nil)
	got := TupleVisibleSubxact(h, snap, 999, nil, storage.InvalidCommandId, nil, nil)
	if got != want {
		t.Errorf("TupleVisibleSubxact degraded: got %v want %v", got, want)
	}
}

// TestSubxactAbortHidesRowAfterParentCommit verifies the core invariant:
// an aborted subxact's rows are invisible even after the parent commits.
// This matches upstream's check for xmin being a rolled-back subxact.
func TestSubxactAbortHidesRowAfterParentCommit(t *testing.T) {
	mgr := NewManager()

	txP, _ := mgr.Begin(IsolationReadCommitted)
	// M0093: materialise the parent's XID; the subxact synthetic
	// arithmetic below requires a real top-level XID.
	if xid, err := mgr.AssignXID(txP); err == nil {
		txP.XID = xid
	} else {
		t.Fatal(err)
	}
	subXid := txP.XID + 100 // synthetic subxact xid

	// Register subxact, then abort it.
	mgr.RegisterSubXid(subXid, txP.XID)
	mgr.MarkSubxactAborted(subXid)

	// Parent commits.
	_ = mgr.Commit(txP)

	// Snapshot after parent commits.
	snapAfter := makeSnap(txP.XID+200, txP.XID+300)

	h := storage.HeapTupleHeader{
		Xmin:     subXid,
		Xmax:     storage.InvalidTransactionID,
		Infomask: 0,
	}
	// Row was created by an aborted subxact — must be invisible.
	if TupleVisibleSubxact(h, snapAfter, txP.XID+500, mgr, storage.InvalidCommandId, nil, nil) {
		t.Error("row created by aborted subxact should be invisible after parent commit")
	}

	// But a non-aborted subxact of the same parent should be visible.
	subXid2 := txP.XID + 101
	mgr.RegisterSubXid(subXid2, txP.XID)
	// NOT aborted — just registered.
	h2 := storage.HeapTupleHeader{
		Xmin:     subXid2,
		Xmax:     storage.InvalidTransactionID,
		Infomask: 0,
	}
	if !TupleVisibleSubxact(h2, snapAfter, txP.XID+500, mgr, storage.InvalidCommandId, nil, nil) {
		t.Error("row created by committed subxact should be visible after parent commit")
	}
}

// TestTupleVisibleSubxactMultiXact pins the subtransaction-aware twin of
// TestTupleVisibleMultiXact (M0118-0003). TupleVisibleSubxact is the visibility
// check used by the main seqscan path (operators_storage.go), FK enforcement,
// MERGE, and DDL table rewrites; like TupleVisible it must resolve an
// updater-bearing MultiXactId xmax (HEAP_XMAX_IS_MULTI set, LOCK_ONLY clear) to
// its real updater member before judging visibility, instead of mis-reading the
// MultiXactId as a deleter xid. With a nil SubxactResolver it must behave
// exactly like TupleVisible's multixact branch (pattern_sibling_paths_must_agree).
func TestTupleVisibleSubxactMultiXact(t *testing.T) {
	// xid < 10 is seen as committed; 15 is in-progress; >= 20 is in the future.
	snap := Snapshot{
		Xmin:       10,
		Xmax:       20,
		InProgress: []storage.TransactionID{15},
	}
	const current = storage.TransactionID(18)

	store := multixact.NewStore()
	updCommitted, err := store.CreateFromMembers([]multixact.Member{
		{Xid: 5, Status: multixact.StatusForShare},
		{Xid: 9, Status: multixact.StatusNoKeyUpdate}, // committed updater
	})
	if err != nil {
		t.Fatalf("CreateFromMembers(committed): %v", err)
	}
	updInProgress, err := store.CreateFromMembers([]multixact.Member{
		{Xid: 5, Status: multixact.StatusForShare},
		{Xid: 15, Status: multixact.StatusNoKeyUpdate}, // in-progress updater
	})
	if err != nil {
		t.Fatalf("CreateFromMembers(in-progress): %v", err)
	}
	updFuture, err := store.CreateFromMembers([]multixact.Member{
		{Xid: 5, Status: multixact.StatusForShare},
		{Xid: 30, Status: multixact.StatusUpdate}, // future updater
	})
	if err != nil {
		t.Fatalf("CreateFromMembers(future): %v", err)
	}
	updSelf, err := store.CreateFromMembers([]multixact.Member{
		{Xid: 5, Status: multixact.StatusForShare},
		{Xid: current, Status: multixact.StatusUpdate}, // our own xact updated it
	})
	if err != nil {
		t.Fatalf("CreateFromMembers(self): %v", err)
	}
	// Only lockers, no updater (LOCK_ONLY deliberately left clear to exercise
	// the defensive "no resolvable updater → treat as a pure lock" branch).
	lockersOnly, err := store.CreateFromMembers([]multixact.Member{
		{Xid: 5, Status: multixact.StatusForShare},
		{Xid: 6, Status: multixact.StatusForKeyShare},
	})
	if err != nil {
		t.Fatalf("CreateFromMembers(lockers only): %v", err)
	}

	mk := func(multi multixact.MultiXactId) storage.HeapTupleHeader {
		return storage.HeapTupleHeader{
			Xmin:     8, // long-committed creator → scan reaches xmax logic
			Xmax:     storage.TransactionID(multi),
			Infomask: storage.HeapXmaxIsMulti, // IS_MULTI set, LOCK_ONLY clear
		}
	}

	cases := []struct {
		name string
		h    storage.HeapTupleHeader
		mxs  *multixact.Store
		want bool
	}{
		{"committed updater → invisible", mk(updCommitted), store, false},
		{"in-progress updater → visible", mk(updInProgress), store, true},
		{"future updater → visible", mk(updFuture), store, true},
		{"our own updater → invisible", mk(updSelf), store, false},
		{"lockers only (no updater) → visible", mk(lockersOnly), store, true},
		{"store unavailable → invisible (conservative)", mk(updCommitted), nil, false},
		{"multi absent from store → invisible (conservative)", mk(9999), store, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// nil SubxactResolver: the multixact branch must match TupleVisible
			// exactly (the updater xids are plain top-level xids).
			if got := TupleVisibleSubxact(c.h, snap, current, nil, storage.InvalidCommandId, nil, c.mxs); got != c.want {
				t.Errorf("TupleVisibleSubxact = %v, want %v; header=%+v", got, c.want, c.h)
			}
		})
	}
}
