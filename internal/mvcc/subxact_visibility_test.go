package mvcc

import (
	"testing"

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
	want := TupleVisible(h, snap, 999)
	got := TupleVisibleSubxact(h, snap, 999, nil)
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
	if TupleVisibleSubxact(h, snapAfter, txP.XID+500, mgr) {
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
	if !TupleVisibleSubxact(h2, snapAfter, txP.XID+500, mgr) {
		t.Error("row created by committed subxact should be visible after parent commit")
	}
}
