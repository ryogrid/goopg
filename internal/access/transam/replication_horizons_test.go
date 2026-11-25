package transam

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestReplicationSlotXminFloorsHorizons pins the physical-slot xmin source
// (PG's replication_slot_xmin, M0149-0077i): it holds OldestXmin back, and
// ReplicationHorizons reports it as the data xmin while the logical slots'
// catalog_xmin comes back separately.
func TestReplicationSlotXminFloorsHorizons(t *testing.T) {
	m := NewManager()
	m.xidgen.SetNext(1000)
	if got := m.OldestXmin(); got != 1000 {
		t.Fatalf("OldestXmin = %d, want 1000", got)
	}
	m.SetReplicationSlotXminSource(func() uint64 { return 700 })
	m.SetCatalogXminSource(func() uint64 { return 600 })
	if got := m.OldestXmin(); got != 600 {
		t.Fatalf("OldestXmin = %d, want 600 (catalog floor)", got)
	}
	xmin, cx := m.ReplicationHorizons()
	if xmin != storage.TransactionID(700) || cx != storage.TransactionID(600) {
		t.Fatalf("ReplicationHorizons = %d, %d; want 700, 600", xmin, cx)
	}
	m.SetReplicationSlotXminSource(nil)
	m.SetCatalogXminSource(nil)
	if xmin, cx := m.ReplicationHorizons(); xmin != 1000 || cx != 0 {
		t.Fatalf("after clearing: %d, %d", xmin, cx)
	}
}
