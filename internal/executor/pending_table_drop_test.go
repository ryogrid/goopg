package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestPendingTableDropSession verifies the BasicSession deferral list for DROP
// TABLE (design 0118-0081, alter-table-4 perm 3): Add/Take round-trip and the
// savepoint-depth cancel so ROLLBACK TO before a deferred drop does not still
// remove the table at the outer COMMIT.
func TestPendingTableDropSession(t *testing.T) {
	s := NewBasicSession()

	t100 := &catalog.Table{OID: 100, Name: "c1"}
	t300 := &catalog.Table{OID: 300, Name: "c2"}
	s.AddPendingTableDrop(PendingTableDrop{Table: t100, SavepointDepth: 0})
	s.AddPendingTableDrop(PendingTableDrop{Table: t300, SavepointDepth: 2})

	// ROLLBACK TO a savepoint at depth 2 discards exactly the depth>=2 entry.
	s.CancelPendingTableDropsToDepth(2)

	got := s.TakePendingTableDrops()
	if len(got) != 1 {
		t.Fatalf("after cancel, Take returned %d entries, want 1", len(got))
	}
	if got[0].Table == nil || got[0].Table.OID != 100 {
		t.Fatalf("surviving entry = %+v, want the depth-0 drop of OID 100", got[0])
	}

	// Take drained the list.
	if more := s.TakePendingTableDrops(); len(more) != 0 {
		t.Fatalf("second Take returned %d entries, want 0", len(more))
	}
}
