package transam

import (
	"reflect"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestPendingStatsDrops pins pgstat_get_transactional_drops (M0149-0032b): a
// commit lists the stats entries of objects the transaction dropped, except
// drops inside a rolled-back subtransaction; an abort lists the entries of
// objects it created; both are forgotten once the transaction ends.
func TestPendingStatsDrops(t *testing.T) {
	m := NewManager()
	tx, err := m.Begin(IsolationReadCommitted)
	if err != nil {
		t.Fatal(err)
	}
	xid, err := m.AssignXID(tx)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := m.AllocateSubXid(xid)
	if err != nil {
		t.Fatal(err)
	}
	rel := func(oid uint64) XactStatsItem { return XactStatsItem{Kind: PgStatKindRelation, DBOid: 5, ObjID: oid} }
	m.AddPendingStatsDrop(xid, PendingStatsDrop{Item: rel(100), Xid: xid})
	m.AddPendingStatsDrop(xid, PendingStatsDrop{Item: rel(101), Xid: sub}) // rolled back below
	m.AddPendingStatsDrop(xid, PendingStatsDrop{Item: rel(102), Xid: xid, IsCreate: true})
	m.AddPendingStatsDrop(xid, PendingStatsDrop{Item: XactStatsItem{Kind: PgStatKindFunction, DBOid: 5, ObjID: 103}, Xid: xid})
	m.MarkSubxactAborted(sub)

	if got, want := m.PendingStatsDrops(xid, true), []XactStatsItem{rel(100), {Kind: PgStatKindFunction, DBOid: 5, ObjID: 103}}; !reflect.DeepEqual(got, want) {
		t.Errorf("commit items = %+v, want %+v", got, want)
	}
	if got, want := m.PendingStatsDrops(xid, false), []XactStatsItem{rel(102)}; !reflect.DeepEqual(got, want) {
		t.Errorf("abort items = %+v, want %+v", got, want)
	}
	var seen []XactStatsItem
	m.SetXactMarkerLogger(func(x storage.TransactionID, kind XactMarker, _ bool) error {
		if x == xid && kind == XactCommit {
			seen = m.PendingStatsDrops(x, true)
		}
		return nil
	})
	if err := m.Commit(tx); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 {
		t.Errorf("commit-record hook saw %d items, want 2", len(seen))
	}
	if got := m.PendingStatsDrops(xid, true); len(got) != 0 {
		t.Errorf("items survive the commit: %+v", got)
	}
}
