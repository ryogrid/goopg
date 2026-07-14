package mvcc

import (
	"errors"
	"path/filepath"
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
		{in: "serializable", want: IsolationSerializable},
		{in: "SERIALIZABLE", want: IsolationSerializable},
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

// TestSerializableDistinctFromRepeatableRead pins the M0104-0001
// contract: ParseIsolationLevel returns a distinct enum value for
// "serializable" so SSI-aware code can branch on it, but Begin must
// accept it and snapshot acquisition currently matches REPEATABLE
// READ semantics (one pinned snapshot for the lifetime of the txn).
func TestSerializableDistinctFromRepeatableRead(t *testing.T) {
	if IsolationSerializable == IsolationRepeatableRead {
		t.Fatal("IsolationSerializable must be distinct from IsolationRepeatableRead")
	}
	if got := IsolationSerializable.String(); got != "serializable" {
		t.Fatalf("IsolationSerializable.String()=%q want %q", got, "serializable")
	}

	m := NewManager()
	txA, err := m.Begin(IsolationSerializable)
	if err != nil {
		t.Fatal(err)
	}
	if txA.Isolation != IsolationSerializable {
		t.Fatalf("Transaction.Isolation=%v want %v", txA.Isolation, IsolationSerializable)
	}
	txB, err := m.Begin(IsolationReadCommitted)
	if err != nil {
		t.Fatal(err)
	}
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
		t.Fatalf("serializable snapshot did not include xid=%d", txB.XID)
	}
	if err := m.Commit(txB); err != nil {
		t.Fatal(err)
	}
	s2, err := m.SnapshotFor(txA)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s1, s2) {
		t.Fatalf("serializable snapshot must pin (RR-like) for M0104-0001\nfirst=%+v\nnext=%+v", s1, s2)
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
	m.SetXactMarkerLogger(func(xid storage.TransactionID, kind XactMarker, _ bool) error {
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
	m.SetXactMarkerLogger(func(xid storage.TransactionID, kind XactMarker, _ bool) error {
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

// TestCommitAsyncPassesWaitLocalFlushFalse pins the M0117-0007 Part B
// contract: Commit and Rollback invoke the xactMarker hook with
// waitLocalFlush=true (the pre-existing, always-synchronous behaviour);
// CommitAsync is the only entry point that passes false, telling the hook it
// may return without waiting for its own local WAL flush.
func TestCommitAsyncPassesWaitLocalFlushFalse(t *testing.T) {
	m := NewManager()
	var got []bool
	m.SetXactMarkerLogger(func(_ storage.TransactionID, _ XactMarker, waitLocalFlush bool) error {
		got = append(got, waitLocalFlush)
		return nil
	})

	beginAndAssign := func() Transaction {
		tx, err := m.Begin(IsolationReadCommitted)
		if err != nil {
			t.Fatal(err)
		}
		xid, err := m.AssignXID(tx)
		if err != nil {
			t.Fatal(err)
		}
		tx.XID = xid
		return tx
	}

	tx1 := beginAndAssign()
	if err := m.Commit(tx1); err != nil {
		t.Fatal(err)
	}
	tx2 := beginAndAssign()
	if err := m.CommitAsync(tx2); err != nil {
		t.Fatal(err)
	}
	tx3 := beginAndAssign()
	if err := m.Rollback(tx3); err != nil {
		t.Fatal(err)
	}

	want := []bool{true, false, true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("waitLocalFlush sequence=%v want=%v (Commit, CommitAsync, Rollback)", got, want)
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
	m.SetXactMarkerLogger(func(_ storage.TransactionID, _ XactMarker, _ bool) error {
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
	m.SetXactMarkerLogger(func(_ storage.TransactionID, _ XactMarker, _ bool) error {
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

// TestOldestXminForProc_SessionLocalIgnoresOtherSnapshots verifies that the
// session-local horizon used by TEMPORARY-relation prune/vacuum (M0118-0009
// horizons) ignores another backend's older snapshot: a concurrent RR reader
// pins the global OldestXmin, yet OldestXminForProc(writer) stays at the
// writer's own (newer) horizon so the writer can reclaim its own temp rows.
func TestOldestXminForProc_SessionLocalIgnoresOtherSnapshots(t *testing.T) {
	m := NewManager()
	// txR is a read-only RR session that takes an early snapshot and holds it.
	txR, _ := m.Begin(IsolationRepeatableRead)
	snapR, err := m.SnapshotFor(txR)
	if err != nil {
		t.Fatal(err)
	}
	// Advance the XID counter so a later session's horizon is strictly newer.
	txW0, _ := m.Begin(IsolationReadCommitted)
	if _, err := m.AssignXID(txW0); err != nil {
		t.Fatal(err)
	}
	_ = m.Commit(txW0)
	// txS is a separate session (the temp-table owner) that takes its snapshot
	// AFTER txR's. Its session-local horizon must not be pinned by txR.
	txS, _ := m.Begin(IsolationReadCommitted)
	snapS, err := m.SnapshotFor(txS)
	if err != nil {
		t.Fatal(err)
	}
	procS := int32(txS.Handle) - 1

	// Global OldestXmin is pinned by txR's older snapshot.
	if g := m.OldestXmin(); g > snapR.Xmin {
		t.Errorf("global OldestXmin=%d not pinned by txR snapshot xmin=%d", g, snapR.Xmin)
	}
	// Session-local horizon ignores txR entirely and tracks txS's own xmin.
	local := m.OldestXminForProc(procS)
	if local < snapR.Xmin {
		t.Errorf("OldestXminForProc=%d unexpectedly below txR snapshot xmin=%d (should ignore other backends)", local, snapR.Xmin)
	}
	if local > snapS.Xmin {
		t.Errorf("OldestXminForProc=%d above txS own snapshot xmin=%d (must respect own snapshot)", local, snapS.Xmin)
	}
	_ = m.Rollback(txS)
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
	m.SetXactMarkerLogger(func(_ storage.TransactionID, _ XactMarker, _ bool) error {
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

// TestClassifyXID covers the snapshot-independent commit classifier amcheck's
// verify_heapam SRF consults (M0110-0003 S5). It asserts the four in-range
// cases plus the out-of-range / invalid sentinels, and that a CLOG-recorded
// abort with no in-memory aborted-set entry still classifies as aborted.
func TestClassifyXID(t *testing.T) {
	m := NewManager()

	// Committed top-level xact: settled, below NextXID, not aborted ⇒ committed.
	txC, err := m.Begin(IsolationReadCommitted)
	if err != nil {
		t.Fatal(err)
	}
	xidC, err := m.AssignXID(txC)
	if err != nil {
		t.Fatal(err)
	}
	txC.XID = xidC
	if err := m.Commit(txC); err != nil {
		t.Fatal(err)
	}

	// Aborted top-level xact: recorded in the in-memory aborted set.
	txA, err := m.Begin(IsolationReadCommitted)
	if err != nil {
		t.Fatal(err)
	}
	xidA, err := m.AssignXID(txA)
	if err != nil {
		t.Fatal(err)
	}
	txA.XID = xidA
	if err := m.Rollback(txA); err != nil {
		t.Fatal(err)
	}

	// In-progress xact: still in the proc array.
	txI, err := m.Begin(IsolationReadCommitted)
	if err != nil {
		t.Fatal(err)
	}
	xidI, err := m.AssignXID(txI)
	if err != nil {
		t.Fatal(err)
	}
	txI.XID = xidI

	cases := []struct {
		name string
		xid  storage.TransactionID
		want XidVisibilityStatus
	}{
		{"committed", xidC, XidVisCommitted},
		{"aborted", xidA, XidVisAborted},
		{"in-progress", xidI, XidVisInProgress},
		{"future/unassigned", m.NextXID() + 5, XidVisUnknown},
		{"invalid", storage.InvalidTransactionID, XidVisUnknown},
	}
	for _, tc := range cases {
		if got := m.ClassifyXID(tc.xid); got != tc.want {
			t.Errorf("ClassifyXID(%s xid=%d) = %v, want %v", tc.name, tc.xid, got, tc.want)
		}
	}
}

// TestClassifyXID_ClogAbortedFallback proves the CLOG path: an xid the in-memory
// aborted set has forgotten (e.g. a recovered abort) but the durable CLOG marks
// aborted still classifies as aborted, not the committed fallback.
func TestClassifyXID_ClogAbortedFallback(t *testing.T) {
	m := NewManager()
	m.SetNextXID(1000) // advance the range so xid 50 is "in-range" (below NextXID)

	dir := t.TempDir()
	clog, err := OpenCLog(filepath.Join(dir, "pg_xact"))
	if err != nil {
		t.Fatalf("OpenCLog: %v", err)
	}
	if err := clog.EnablePGSLRUMirror(filepath.Join(dir, "pg_xact_slru")); err != nil {
		t.Fatalf("EnablePGSLRUMirror: %v", err)
	}
	m.SetCLog(clog)

	const recoveredAbort = storage.TransactionID(50)
	if err := clog.SetAborted(recoveredAbort); err != nil {
		t.Fatal(err)
	}
	if got := m.ClassifyXID(recoveredAbort); got != XidVisAborted {
		t.Errorf("ClassifyXID(clog-aborted xid=%d) = %v, want XidVisAborted", recoveredAbort, got)
	}

	const recoveredCommit = storage.TransactionID(60)
	if err := clog.SetCommitted(recoveredCommit); err != nil {
		t.Fatal(err)
	}
	if got := m.ClassifyXID(recoveredCommit); got != XidVisCommitted {
		t.Errorf("ClassifyXID(clog-committed xid=%d) = %v, want XidVisCommitted", recoveredCommit, got)
	}
}

// TestDetachToDedicatedSlot verifies that relocating a prepared transaction off
// its originating backend's proc slot keeps its XID visible as in-progress (so
// its writes stay invisible), frees the original slot for reuse without
// clobbering the relocated transaction, and lets the relocated handle be
// committed afterwards. M0118-0009 (stats — cross-backend two-phase commit).
func TestDetachToDedicatedSlot(t *testing.T) {
	m := NewManager()
	// Originating backend's transaction with a materialised (writing) XID.
	tx, err := m.Begin(IsolationReadCommitted, 5)
	if err != nil {
		t.Fatal(err)
	}
	xid, err := m.AssignXID(tx)
	if err != nil {
		t.Fatal(err)
	}
	tx.XID = xid

	moved, err := m.DetachToDedicatedSlot(tx)
	if err != nil {
		t.Fatalf("DetachToDedicatedSlot: %v", err)
	}
	if moved.Handle == tx.Handle {
		t.Fatalf("expected a new handle, got the original %d", moved.Handle)
	}
	if moved.XID != xid {
		t.Fatalf("relocated XID = %d, want %d", moved.XID, xid)
	}
	// The dedicated slot must live in the reserved high region.
	if int(moved.Handle)-1 < ConnSlotCount {
		t.Fatalf("dedicated slot %d not in reserved region [%d, %d)", int(moved.Handle)-1, ConnSlotCount, DefaultProcArraySize)
	}

	// The relocated XID must still read as in-progress in a fresh snapshot.
	observer, err := m.Begin(IsolationReadCommitted, 6)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := m.SnapshotFor(observer)
	if err != nil {
		t.Fatal(err)
	}
	if !snap.HasInProgress(xid) {
		t.Fatalf("relocated prepared xid=%d not in-progress after detach", xid)
	}

	// The originating backend may reuse its freed slot (procNum 5) without
	// disturbing the relocated transaction.
	reuse, err := m.Begin(IsolationReadCommitted, 5)
	if err != nil {
		t.Fatalf("reuse of freed origin slot: %v", err)
	}
	if err := m.Commit(reuse); err != nil {
		t.Fatalf("commit of reuse txn: %v", err)
	}
	// The relocated prepared transaction is still in-progress.
	snap2, err := m.SnapshotFor(observer)
	if err != nil {
		t.Fatal(err)
	}
	if !snap2.HasInProgress(xid) {
		t.Fatalf("relocated xid=%d lost in-progress after origin-slot reuse", xid)
	}

	// Finalising the relocated handle commits the prepared transaction.
	if err := m.Commit(moved); err != nil {
		t.Fatalf("commit relocated prepared txn: %v", err)
	}
	snap3, err := m.SnapshotFor(observer)
	if err != nil {
		t.Fatal(err)
	}
	if snap3.HasInProgress(xid) {
		t.Fatalf("xid=%d still in-progress after COMMIT PREPARED", xid)
	}
	_ = m.Commit(observer)
}

// TestDetachToDedicatedSlotRejectsSerializable verifies SERIALIZABLE detach is
// refused (its SSI bookkeeping is Handle-keyed). M0118-0009.
func TestDetachToDedicatedSlotRejectsSerializable(t *testing.T) {
	m := NewManager()
	tx, err := m.Begin(IsolationSerializable, 7)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.DetachToDedicatedSlot(tx); !errors.Is(err, ErrUnsupportedDetach) {
		t.Fatalf("expected ErrUnsupportedDetach, got %v", err)
	}
	_ = m.Rollback(tx)
}

// TestOldestXminFoldsCatalogXminSource pins the retention consumer: once a
// catalog_xmin source is installed (server wires it to
// wal.Slots.MinCatalogXmin), OldestXmin holds the global pruning/truncation
// horizon back to the oldest catalog_xmin pinned by a logical slot, but never
// advances it forward past the natural horizon. This is what stops heap
// pruning / VACUUM / CLOG truncation from reclaiming catalog tuple versions an
// in-flight logical decoder still needs.
func TestOldestXminFoldsCatalogXminSource(t *testing.T) {
	m := NewManager()
	// Advance nextXID so the natural (no-txn) horizon is well above the first
	// normal XID, giving room for a lower catalog_xmin to floor it.
	for i := 0; i < 100; i++ {
		m.xidgen.Allocate()
	}
	base := m.OldestXmin()
	if base == 0 {
		t.Fatalf("baseline OldestXmin = 0, expected the running nextXID")
	}

	// A source pinning an OLDER xid floors the horizon.
	older := uint64(base) - 40
	m.SetCatalogXminSource(func() uint64 { return older })
	if got := m.OldestXmin(); uint64(got) != older {
		t.Fatalf("OldestXmin with older catalog_xmin = %d, want %d", got, older)
	}

	// A source pinning a NEWER xid must not advance the horizon forward
	// (retention only ever holds back, never reclaims more).
	m.SetCatalogXminSource(func() uint64 { return uint64(base) + 40 })
	if got := m.OldestXmin(); got != base {
		t.Fatalf("OldestXmin with newer catalog_xmin = %d, want unchanged %d", got, base)
	}

	// A source returning 0 (no slot pinning) is a no-op.
	m.SetCatalogXminSource(func() uint64 { return 0 })
	if got := m.OldestXmin(); got != base {
		t.Fatalf("OldestXmin with zero catalog_xmin = %d, want %d", got, base)
	}

	// Clearing the source restores the pure in-memory behaviour.
	m.SetCatalogXminSource(nil)
	if got := m.OldestXmin(); got != base {
		t.Fatalf("OldestXmin after clearing source = %d, want %d", got, base)
	}
}

// TestCommit_XactMarkerErrorFailsCommitAndStaysInProgress is the C2-S3
// fault-injection anchor for the sync-commit flush contract: since the cut,
// EVERY flush error on the sync path (including wal.ErrLSNNotWritten, which
// the xact-marker logger used to swallow) is returned from the hook, and
// Commit must (a) surface the error — the client is never acked — and
// (b) leave the transaction in-progress so no reader observes it committed.
// Crash safety follows: an un-acked, in-progress txn is blanket-aborted by
// MarkUnknownAsAborted on restart, with no durable commit record to
// resurrect it.
func TestCommit_XactMarkerErrorFailsCommitAndStaysInProgress(t *testing.T) {
	m := NewManager()
	injected := errors.New("injected: sync commit flush: LSN not written")
	m.SetXactMarkerLogger(func(_ storage.TransactionID, kind XactMarker, waitLocalFlush bool) error {
		if kind == XactCommit && waitLocalFlush {
			return injected
		}
		return nil
	})
	tx, _ := m.Begin(IsolationReadCommitted)
	// Materialize a real XID so the hook fires (read-only commits skip it).
	xid, err := m.AssignXID(tx)
	if err != nil {
		t.Fatalf("AssignXID: %v", err)
	}
	err = m.Commit(tx)
	if err == nil {
		t.Fatal("Commit succeeded despite a failing sync-commit flush — the client would be acked for a non-durable commit")
	}
	if !errors.Is(err, injected) {
		t.Fatalf("Commit error = %v, want the injected flush error surfaced", err)
	}
	// The txn must still be in-progress for snapshots (finish propagates the
	// hook error BEFORE the active-set removal).
	if !m.IsXIDActive(xid) {
		t.Fatalf("xid %d no longer active after a failed sync-commit flush — it must stay in-progress", xid)
	}
}

// TestConnSlotChurnDoesNotClobberLiveSlots pins the AcquireConnSlot fix:
// the historical (pid-1)%ConnSlotCount assignment wrapped after
// ConnSlotCount cumulative connections and handed a LIVE session's slot to
// a new connection (soak finding: "mvcc: unknown transaction" storms once
// a 5 conn/s sampler pushed the counter past ~1000 at ~180s). Churning
// far more acquire/release cycles than the array size must never touch a
// held slot.
func TestConnSlotChurnDoesNotClobberLiveSlots(t *testing.T) {
	m := NewManager()
	held, err := m.AcquireConnSlot()
	if err != nil {
		t.Fatal(err)
	}
	tx, err := m.Begin(IsolationReadCommitted, held)
	if err != nil {
		t.Fatalf("Begin(at %d): %v", held, err)
	}
	for i := 0; i < 5*DefaultProcArraySize; i++ {
		p, err := m.AcquireConnSlot()
		if err != nil {
			t.Fatalf("churn acquire %d: %v", i, err)
		}
		if p == held {
			t.Fatalf("churn handed out the HELD slot %d at cycle %d", held, i)
		}
		m.ReleaseConnSlot(p)
	}
	// The long-lived session's transaction is still intact.
	if _, err := m.SnapshotFor(tx); err != nil {
		t.Fatalf("held session's txn lost after churn: %v", err)
	}
	if err := m.Commit(tx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	m.ReleaseConnSlot(held)
}
