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
	m.SetXactMarkerLogger(func(xid storage.TransactionID, kind XactMarker) error {
		got = kind
		return nil
	})
	tx, _ := m.Begin(IsolationReadCommitted)
	if err := m.Rollback(tx); err != nil {
		t.Fatal(err)
	}
	if got != XactAbort {
		t.Errorf("kind=%v want XactAbort", got)
	}
}

// TestXactMarkerLoggerErrorAbortsCommit: when the hook fails
// (e.g. WAL append errors out), Commit must surface the error
// and the txn must remain in-progress so the caller can retry
// or escalate.
func TestXactMarkerLoggerErrorAbortsCommit(t *testing.T) {
	m := NewManager()
	wantErr := errors.New("wal: out of disk")
	m.SetXactMarkerLogger(func(_ storage.TransactionID, _ XactMarker) error {
		return wantErr
	})
	tx, _ := m.Begin(IsolationReadCommitted)
	if err := m.Commit(tx); err == nil || !errors.Is(err, wantErr) {
		t.Errorf("Commit err=%v want %v", err, wantErr)
	}
	// Txn must still be in-progress (not removed from active set).
	if got := m.ActiveCount(); got != 1 {
		t.Errorf("ActiveCount=%d want 1 (commit failed, tx still in-progress)", got)
	}
}
