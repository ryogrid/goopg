package transam

import (
	"reflect"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestPendingRelDeletes pins the PendingRelDelete lifecycle
// (catalog/storage.c): a commit lists and then removes what the transaction
// dropped, except drops made inside a rolled-back subtransaction (or below
// one); an abort removes nothing; a relation dropped twice is listed once.
func TestPendingRelDeletes(t *testing.T) {
	m := NewManager()
	rel := func(n uint32) storage.RelFileNode { return storage.RelFileNode{DBOid: 5, RelOid: n} }
	begin := func() (Transaction, storage.TransactionID) {
		t.Helper()
		tx, err := m.Begin(IsolationReadCommitted)
		if err != nil {
			t.Fatal(err)
		}
		xid, err := m.AssignXID(tx)
		if err != nil {
			t.Fatal(err)
		}
		return tx, xid
	}
	var removed []uint32
	add := func(top, owner storage.TransactionID, n uint32) {
		m.AddPendingRelDelete(top, PendingRelDelete{Rel: rel(n), Xid: owner, Drop: func() { removed = append(removed, n) }})
	}

	// Commit: listed for the commit record, removed after it, in drop order.
	tx, xid := begin()
	sub, err := m.AllocateSubXid(xid)
	if err != nil {
		t.Fatal(err)
	}
	inner, err := m.AllocateSubXid(sub)
	if err != nil {
		t.Fatal(err)
	}
	add(xid, xid, 100)
	add(xid, inner, 101) // below the subtransaction rolled back next
	add(xid, xid, 100)   // dropped twice: listed once
	add(xid, xid, 102)
	m.MarkSubxactAborted(sub)
	var seen []storage.RelFileNode
	m.SetXactMarkerLogger(func(x storage.TransactionID, kind XactMarker, _ bool) error {
		if x == xid && kind == XactCommit {
			seen = m.PendingRelDeletes(x)
			if len(removed) != 0 {
				t.Errorf("storage removed before the commit record: %v", removed)
			}
		}
		return nil
	})
	if err := m.Commit(tx); err != nil {
		t.Fatal(err)
	}
	if want := []storage.RelFileNode{rel(100), rel(102)}; !reflect.DeepEqual(seen, want) {
		t.Errorf("commit record lists %v, want %v", seen, want)
	}
	if want := []uint32{100, 102}; !reflect.DeepEqual(removed, want) {
		t.Errorf("removed at commit %v, want %v", removed, want)
	}
	if got := m.PendingRelDeletes(xid); len(got) != 0 {
		t.Errorf("entries left after commit: %v", got)
	}

	// Abort: nothing is removed and nothing is left behind.
	removed = nil
	tx, xid = begin()
	add(xid, xid, 200)
	if err := m.Rollback(tx); err != nil {
		t.Fatal(err)
	}
	if len(removed) != 0 || len(m.PendingRelDeletes(xid)) != 0 {
		t.Errorf("abort removed %v, left %v", removed, m.PendingRelDeletes(xid))
	}
}
