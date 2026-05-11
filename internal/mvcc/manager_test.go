package mvcc

import (
	"errors"
	"reflect"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

func TestParseIsolationLevel(t *testing.T) {
	tests := []struct {
		in   string
		want IsolationLevel
	}{
		{in: "read committed", want: IsolationReadCommitted},
		{in: "READ UNCOMMITTED", want: IsolationReadCommitted},
		{in: "repeatable read", want: IsolationRepeatableRead},
		{in: "serializable", want: IsolationRepeatableRead},
	}
	for _, tc := range tests {
		got, err := ParseIsolationLevel(tc.in)
		if err != nil {
			t.Fatalf("ParseIsolationLevel(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("ParseIsolationLevel(%q)=%v want=%v", tc.in, got, tc.want)
		}
	}
	if _, err := ParseIsolationLevel("bogus"); err == nil {
		t.Fatal("ParseIsolationLevel(bogus) expected error")
	}
}

func TestReadCommittedGetsFreshSnapshots(t *testing.T) {
	m := NewManager()
	txA, err := m.Begin(IsolationReadCommitted)
	if err != nil {
		t.Fatal(err)
	}
	txB, err := m.Begin(IsolationReadCommitted)
	if err != nil {
		t.Fatal(err)
	}
	// M0093: AssignXID promotes txB to a real XID so the snapshot
	// includes it in InProgress (read-only txns are deliberately
	// excluded from InProgress — they have no XID to track).
	xidB, err := m.AssignXID(txB)
	if err != nil {
		t.Fatal(err)
	}
	txB.XID = xidB

	s1, err := m.SnapshotFor(txA)
	if err != nil {
		t.Fatal(err)
	}
	if !s1.HasInProgress(txB.XID) {
		t.Fatalf("snapshot did not include concurrent xid=%d", txB.XID)
	}

	if err := m.Commit(txB); err != nil {
		t.Fatal(err)
	}
	s2, err := m.SnapshotFor(txA)
	if err != nil {
		t.Fatal(err)
	}
	if s2.HasInProgress(txB.XID) {
		t.Fatalf("read committed snapshot still contains committed xid=%d", txB.XID)
	}

	if err := m.Commit(txA); err != nil {
		t.Fatal(err)
	}
}

func TestRepeatableReadPinsFirstSnapshot(t *testing.T) {
	m := NewManager()
	txA, err := m.Begin(IsolationRepeatableRead)
	if err != nil {
		t.Fatal(err)
	}
	txB, err := m.Begin(IsolationReadCommitted)
	if err != nil {
		t.Fatal(err)
	}
	// M0093: AssignXID to make txB show up in InProgress.
	xidB, err := m.AssignXID(txB)
	if err != nil {
		t.Fatal(err)
	}
	txB.XID = xidB

	s1, err := m.SnapshotFor(txA)
	if err != nil {
		t.Fatal(err)
	}
	if !s1.HasInProgress(txB.XID) {
		t.Fatalf("first snapshot did not include xid=%d", txB.XID)
	}

	if err := m.Commit(txB); err != nil {
		t.Fatal(err)
	}
	txC, err := m.Begin(IsolationReadCommitted)
	if err != nil {
		t.Fatal(err)
	}

	s2, err := m.SnapshotFor(txA)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s1, s2) {
		t.Fatalf("repeatable read snapshot changed\nfirst=%+v\nnext=%+v", s1, s2)
	}

	if err := m.Commit(txC); err != nil {
		t.Fatal(err)
	}
	if err := m.Commit(txA); err != nil {
		t.Fatal(err)
	}
}

func TestFinishUnknownTransaction(t *testing.T) {
	m := NewManager()
	tx, err := m.Begin(IsolationReadCommitted)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Rollback(tx); err != nil {
		t.Fatal(err)
	}
	if err := m.Commit(tx); !errors.Is(err, ErrUnknownTransaction) {
		t.Fatalf("err=%v want ErrUnknownTransaction", err)
	}
}

// TestXactMarkerLoggerCalledOnCommit pins the M0008 hook
// contract: a successful Commit invokes the logger with
// kind=XactCommit and the xact's xid before removing it from the
// active set. The hook is the seam the wire layer uses to emit
// EncodeXactCommit records into the WAL stream.
func TestXactMarkerLoggerCalledOnCommit(t *testing.T) {
	m := NewManager()
	type call struct {
		xid  storage.TransactionID
		kind XactMarker
	}
	var calls []call
	m.SetXactMarkerLogger(func(xid storage.TransactionID, kind XactMarker) error {
		calls = append(calls, call{xid, kind})
		return nil
	})
	tx, err := m.Begin(IsolationReadCommitted)
	if err != nil {
		t.Fatal(err)
	}
	// M0093: the hook only fires when an XID was materialised
	// (i.e. when the txn actually did writes). Assign one to
	// preserve the original assertion intent.
	xid, err := m.AssignXID(tx)
	if err != nil {
		t.Fatal(err)
	}
	tx.XID = xid
	if err := m.Commit(tx); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls=%v want one entry", calls)
	}
	if calls[0].xid != tx.XID || calls[0].kind != XactCommit {
		t.Errorf("call=%+v want {xid=%d, XactCommit}", calls[0], tx.XID)
	}
}

// TestXactMarkerLoggerCalledOnRollback: same hook contract but
// for Rollback → kind=XactAbort.
func TestXactMarkerLoggerCalledOnRollback(t *testing.T) {
	m := NewManager()
	var got XactMarker
	gotSet := false
	m.SetXactMarkerLogger(func(xid storage.TransactionID, kind XactMarker) error {
		got = kind
		gotSet = true
		return nil
	})
	tx, _ := m.Begin(IsolationReadCommitted)
	// M0093: materialise XID so the abort hook fires.
	xid, err := m.AssignXID(tx)
	if err != nil {
		t.Fatal(err)
	}
	tx.XID = xid
	if err := m.Rollback(tx); err != nil {
		t.Fatal(err)
	}
	if !gotSet {
		t.Fatalf("hook was not invoked")
	}
	if got != XactAbort {
		t.Errorf("kind=%v want XactAbort", got)
	}
}

// TestXactMarkerLoggerErrorAbortsCommit: when the hook fails
// (e.g. WAL append errors out), Commit must surface the error
// and the txn must remain in-progress so the caller can retry
// or escalate.
// ============================================================
// M0093 — lazy XID assignment tests (Design B).
// ============================================================

// TestBegin_DoesNotAssignXID — Begin returns a Transaction with a
// non-zero Handle but XID == InvalidTransactionID. Mirrors PG's
// lazy-XID-allocation invariant: read-only txns never consume an
// XID slot.
func TestBegin_DoesNotAssignXID(t *testing.T) {
	m := NewManager()
	tx, err := m.Begin(IsolationReadCommitted)
	if err != nil {
		t.Fatal(err)
	}
	if tx.Handle == 0 {
		t.Errorf("Handle=0 after Begin (want non-zero)")
	}
	if tx.XID != storage.InvalidTransactionID {
		t.Errorf("XID=%d after Begin (want InvalidTransactionID)", tx.XID)
	}
	if next := m.NextXID(); next != FirstNormalTransactionID {
		t.Errorf("nextXID=%d after Begin (want %d; XID counter must not advance)", next, FirstNormalTransactionID)
	}
}

// TestCommit_ReadOnlyDoesNotInvokeXactMarker pins the M0093
// semantics: a read-only transaction (no AssignXID call) skips the
// xactMarker hook entirely on Commit. No WAL XactCommit record, no
// fsync, no clog write.
func TestCommit_ReadOnlyDoesNotInvokeXactMarker(t *testing.T) {
	m := NewManager()
	calls := 0
	m.SetXactMarkerLogger(func(_ storage.TransactionID, _ XactMarker) error {
		calls++
		return nil
	})
	tx, _ := m.Begin(IsolationReadCommitted)
	if err := m.Commit(tx); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Errorf("hook fired %d times for read-only commit (want 0)", calls)
	}
}

// TestRollback_ReadOnlyDoesNotInvokeXactMarker — same as Commit but
// for the Rollback path.
func TestRollback_ReadOnlyDoesNotInvokeXactMarker(t *testing.T) {
	m := NewManager()
	calls := 0
	m.SetXactMarkerLogger(func(_ storage.TransactionID, _ XactMarker) error {
		calls++
		return nil
	})
	tx, _ := m.Begin(IsolationReadCommitted)
	if err := m.Rollback(tx); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Errorf("hook fired %d times for read-only rollback (want 0)", calls)
	}
}

// TestAssignXID_Idempotent — second AssignXID returns the existing
// XID without advancing the counter.
func TestAssignXID_Idempotent(t *testing.T) {
	m := NewManager()
	tx, _ := m.Begin(IsolationReadCommitted)
	xid1, err := m.AssignXID(tx)
	if err != nil {
		t.Fatal(err)
	}
	xid2, err := m.AssignXID(tx)
	if err != nil {
		t.Fatal(err)
	}
	if xid1 != xid2 {
		t.Errorf("AssignXID not idempotent: xid1=%d xid2=%d", xid1, xid2)
	}
	if next := m.NextXID(); next != xid1+1 {
		t.Errorf("nextXID=%d after idempotent AssignXID (want %d; counter advanced twice)", next, xid1+1)
	}
}

// TestAssignXID_DistinctXIDs — two concurrent transactions get
// distinct XIDs.
func TestAssignXID_DistinctXIDs(t *testing.T) {
	m := NewManager()
	txA, _ := m.Begin(IsolationReadCommitted)
	txB, _ := m.Begin(IsolationReadCommitted)
	xidA, _ := m.AssignXID(txA)
	xidB, _ := m.AssignXID(txB)
	if xidA == xidB {
		t.Errorf("two AssignXID calls returned same xid=%d", xidA)
	}
}

// TestSnapshot_ExcludesReadOnlyTxns — read-only transactions are
// NOT included in another transaction's snapshot InProgress list,
// because they have no XID to be visible-as-in-progress.
func TestSnapshot_ExcludesReadOnlyTxns(t *testing.T) {
	m := NewManager()
	// txA is a writer with a materialised XID. txB and txC are
	// read-only — they should be invisible to txA's snapshot.
	txA, _ := m.Begin(IsolationReadCommitted)
	xidA, _ := m.AssignXID(txA)
	_, _ = m.Begin(IsolationReadCommitted) // read-only txB
	_, _ = m.Begin(IsolationReadCommitted) // read-only txC
	snap, err := m.SnapshotFor(txA)
	if err != nil {
		t.Fatal(err)
	}
	// Only txA's own XID may appear in InProgress; the two
	// read-only txns must be excluded.
	if len(snap.InProgress) != 1 || snap.InProgress[0] != xidA {
		t.Errorf("InProgress=%v want exactly [%d] (read-only txns must be excluded)", snap.InProgress, xidA)
	}
}

// TestSnapshot_IncludesAfterAssignXID — after a previously read-only
// txn calls AssignXID, the next snapshot DOES include it.
func TestSnapshot_IncludesAfterAssignXID(t *testing.T) {
	m := NewManager()
	txA, _ := m.Begin(IsolationReadCommitted)
	txB, _ := m.Begin(IsolationReadCommitted)
	xidB, _ := m.AssignXID(txB)
	txB.XID = xidB
	snap, err := m.SnapshotFor(txA)
	if err != nil {
		t.Fatal(err)
	}
	if !snap.HasInProgress(xidB) {
		t.Errorf("snapshot did not include xid=%d after AssignXID", xidB)
	}
}

// TestOldestXmin_PreservedByReadOnlySnapshotXmin — a long-running
// read-only REPEATABLE READ transaction pins OldestXmin via its
// snapshot's Xmin even though it has no XID of its own. Mandatory
// VACUUM correctness invariant (M0093 R-B6).
func TestOldestXmin_PreservedByReadOnlySnapshotXmin(t *testing.T) {
	m := NewManager()
	// txW is a writer that gets an XID and is still in-progress.
	txW, _ := m.Begin(IsolationReadCommitted)
	xidW, _ := m.AssignXID(txW)
	txW.XID = xidW
	// txR is a read-only RR transaction that takes a snapshot
	// before txW commits.
	txR, _ := m.Begin(IsolationRepeatableRead)
	snapR, err := m.SnapshotFor(txR)
	if err != nil {
		t.Fatal(err)
	}
	// txW commits — its xid is no longer active.
	if err := m.Commit(txW); err != nil {
		t.Fatal(err)
	}
	// OldestXmin should still be pinned by snapR.Xmin, because
	// txR's snapshot is still observing tuples xmin >= snapR.Xmin.
	got := m.OldestXmin()
	if got > snapR.Xmin {
		t.Errorf("OldestXmin=%d > snapR.Xmin=%d (read-only RR snapshot not preserving VACUUM horizon)", got, snapR.Xmin)
	}
	_ = m.Rollback(txR)
}

// TestActiveSet_HandlesNotXIDsKey — multiple read-only txns coexist
// without collision, even though they all have XID == 0.
func TestActiveSet_HandlesNotXIDsKey(t *testing.T) {
	m := NewManager()
	const N = 8
	txs := make([]Transaction, N)
	for i := 0; i < N; i++ {
		var err error
		txs[i], err = m.Begin(IsolationReadCommitted)
		if err != nil {
			t.Fatal(err)
		}
		if txs[i].XID != storage.InvalidTransactionID {
			t.Errorf("tx[%d].XID=%d (want Invalid for read-only)", i, txs[i].XID)
		}
	}
	if got := m.ActiveCount(); got != N {
		t.Errorf("ActiveCount=%d want %d (handle-keyed active set must not collide)", got, N)
	}
	for _, tx := range txs {
		_ = m.Commit(tx)
	}
}

func TestXactMarkerLoggerErrorAbortsCommit(t *testing.T) {
	m := NewManager()
	wantErr := errors.New("wal: out of disk")
	m.SetXactMarkerLogger(func(_ storage.TransactionID, _ XactMarker) error {
		return wantErr
	})
	tx, _ := m.Begin(IsolationReadCommitted)
	// M0093: hook fires only when XID is materialised, so assign
	// one to drive the hook-error path under test.
	xid, err := m.AssignXID(tx)
	if err != nil {
		t.Fatal(err)
	}
	tx.XID = xid
	if err := m.Commit(tx); err == nil || !errors.Is(err, wantErr) {
		t.Errorf("Commit err=%v want %v", err, wantErr)
	}
	// Txn must still be in-progress (not removed from active set).
	if got := m.ActiveCount(); got != 1 {
		t.Errorf("ActiveCount=%d want 1 (commit failed, tx still in-progress)", got)
	}
}
