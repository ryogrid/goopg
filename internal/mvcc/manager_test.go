package mvcc

import (
	"errors"
	"reflect"
	"testing"
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
