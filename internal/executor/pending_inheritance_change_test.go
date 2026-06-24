package executor

import "testing"

// TestPendingInheritanceChangeSession verifies the BasicSession deferral list for
// ALTER TABLE {NO} INHERIT (design 0118-0080): Add/Take round-trip and the
// savepoint-depth cancel that returns the count discarded so the caller can drop
// the matching catalog pending-change marks.
func TestPendingInheritanceChangeSession(t *testing.T) {
	s := NewBasicSession()

	s.AddPendingInheritanceChange(PendingInheritanceChange{ParentOID: 100, ChildOID: 200, NoInherit: true, SavepointDepth: 0})
	s.AddPendingInheritanceChange(PendingInheritanceChange{ParentOID: 100, ChildOID: 300, NoInherit: false, SavepointDepth: 2})

	// ROLLBACK TO a savepoint at depth 2 discards exactly the depth>=2 entry.
	if cancelled := s.CancelPendingInheritanceChangesToDepth(2); cancelled != 1 {
		t.Fatalf("CancelPendingInheritanceChangesToDepth(2) = %d, want 1", cancelled)
	}

	got := s.TakePendingInheritanceChanges()
	if len(got) != 1 {
		t.Fatalf("after cancel, Take returned %d entries, want 1", len(got))
	}
	if got[0].ChildOID != 200 || !got[0].NoInherit {
		t.Fatalf("surviving entry = %+v, want the depth-0 NO INHERIT of child 200", got[0])
	}

	// Take drained the list.
	if more := s.TakePendingInheritanceChanges(); len(more) != 0 {
		t.Fatalf("second Take returned %d entries, want 0", len(more))
	}
}
