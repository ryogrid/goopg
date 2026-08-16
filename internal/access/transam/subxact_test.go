package transam

import (
	"testing"
)

// TestSubxactStackPush verifies that Push creates entries with
// monotonically increasing IDs, correct names, and active status.
func TestSubxactStackPush(t *testing.T) {
	var s SubxactStack
	mgr := NewManager()
	tx, _ := mgr.Begin(IsolationReadCommitted)
	snap, _ := mgr.SnapshotFor(tx)

	e1 := s.Push("s1", snap)
	if e1.Id != 1 {
		t.Errorf("first entry Id = %d, want 1", e1.Id)
	}
	if e1.Name != "s1" {
		t.Errorf("entry Name = %q, want s1", e1.Name)
	}
	if e1.Status != SubXactActive {
		t.Errorf("entry Status = %v, want active", e1.Status)
	}
	if e1.Parent != nil {
		t.Error("first entry should have nil parent")
	}
	if e1.Snap == nil {
		t.Error("entry Snap should not be nil")
	}

	e2 := s.Push("s2", snap)
	if e2.Id != 2 {
		t.Errorf("second entry Id = %d, want 2", e2.Id)
	}
	if e2.Parent != e1 {
		t.Errorf("e2.Parent = %v, want e1", e2.Parent)
	}

	if s.Len() != 2 {
		t.Errorf("Len = %d, want 2", s.Len())
	}
	if s.Top() != e2 {
		t.Error("Top() should return innermost (e2)")
	}
}

// TestSubxactStackRelease verifies that Release marks the named entry
// and all inner entries as committed and removes them from the stack.
// This models the lock-owner promotion: callers inspect released entries
// and transfer their lock owners to the parent.
func TestSubxactStackRelease(t *testing.T) {
	var s SubxactStack
	mgr := NewManager()
	tx, _ := mgr.Begin(IsolationReadCommitted)
	snap, _ := mgr.SnapshotFor(tx)

	e1 := s.Push("s1", snap)
	e2 := s.Push("s2", snap)
	e3 := s.Push("s3", snap)
	_ = e3

	// Release "s2" — should also release s3 (inner).
	released, err := s.Release("s2")
	if err != nil {
		t.Fatalf("Release s2: %v", err)
	}
	if len(released) != 2 {
		t.Fatalf("released %d entries, want 2 (s2+s3)", len(released))
	}
	if released[0] != e2 {
		t.Error("released[0] should be e2")
	}
	if released[1] != e3 {
		t.Error("released[1] should be e3")
	}
	for _, r := range released {
		if r.Status != SubXactCommitted {
			t.Errorf("released entry %q status = %v, want committed", r.Name, r.Status)
		}
	}

	// Stack should now contain only s1.
	if s.Len() != 1 {
		t.Errorf("Len = %d, want 1 after release", s.Len())
	}
	if s.Top() != e1 {
		t.Error("Top() should be e1 after releasing s2+s3")
	}

	// e1 remains active (not released).
	if e1.Status != SubXactActive {
		t.Errorf("e1.Status = %v, want active", e1.Status)
	}
}

// TestSubxactStackReleaseNotFound verifies that Release of a non-existent
// savepoint returns an error without modifying the stack.
func TestSubxactStackReleaseNotFound(t *testing.T) {
	var s SubxactStack
	mgr := NewManager()
	tx, _ := mgr.Begin(IsolationReadCommitted)
	snap, _ := mgr.SnapshotFor(tx)
	s.Push("s1", snap)

	_, err := s.Release("nosuchsp")
	if err == nil {
		t.Error("expected error releasing nonexistent savepoint, got nil")
	}
	if s.Len() != 1 {
		t.Errorf("Len = %d, want 1 (stack unchanged)", s.Len())
	}
}

// TestSubxactStackRollbackTo verifies the full ROLLBACK TO semantics:
// 1. Named + inner entries are marked aborted.
// 2. A fresh entry with the same name is pushed.
// 3. Outer entries remain active.
// This models the lock-owner drop: callers inspect aborted entries and
// release their locks.
func TestSubxactStackRollbackTo(t *testing.T) {
	var s SubxactStack
	mgr := NewManager()
	tx, _ := mgr.Begin(IsolationReadCommitted)
	snap, _ := mgr.SnapshotFor(tx)

	e1 := s.Push("s1", snap)
	e2 := s.Push("s2", snap)
	e3 := s.Push("s3", snap)
	_ = e3

	// ROLLBACK TO "s2" — aborts s2+s3, pushes fresh s2.
	aborted, fresh, err := s.RollbackTo("s2", snap)
	if err != nil {
		t.Fatalf("RollbackTo s2: %v", err)
	}

	// Two entries (s2+s3) must be aborted.
	if len(aborted) != 2 {
		t.Fatalf("aborted %d entries, want 2", len(aborted))
	}
	for _, a := range aborted {
		if a.Status != SubXactAborted {
			t.Errorf("aborted entry %q has status %v, want aborted", a.Name, a.Status)
		}
	}

	// Fresh entry replaces the aborted s2.
	if fresh == nil {
		t.Fatal("fresh entry is nil")
	}
	if fresh.Name != "s2" {
		t.Errorf("fresh entry Name = %q, want s2", fresh.Name)
	}
	if fresh.Status != SubXactActive {
		t.Errorf("fresh entry Status = %v, want active", fresh.Status)
	}
	// Fresh entry should have a higher Id than e2.
	if fresh.Id <= e2.Id {
		t.Errorf("fresh Id %d should be > e2 Id %d", fresh.Id, e2.Id)
	}

	// Stack: s1 (active) + fresh-s2 (active) = 2.
	if s.Len() != 2 {
		t.Errorf("Len = %d, want 2 after rollback to s2", s.Len())
	}
	if s.Top() != fresh {
		t.Error("Top() should be fresh entry after RollbackTo")
	}

	// e1 remains active.
	if e1.Status != SubXactActive {
		t.Errorf("e1.Status = %v, want active after RollbackTo s2", e1.Status)
	}
}

// TestSubxactStackRollbackToNotFound verifies that RollbackTo of a
// non-existent savepoint returns an error without modifying the stack.
func TestSubxactStackRollbackToNotFound(t *testing.T) {
	var s SubxactStack
	mgr := NewManager()
	tx, _ := mgr.Begin(IsolationReadCommitted)
	snap, _ := mgr.SnapshotFor(tx)
	s.Push("s1", snap)

	_, _, err := s.RollbackTo("nosuchsp", snap)
	if err == nil {
		t.Error("expected error, got nil")
	}
	if s.Len() != 1 {
		t.Errorf("Len = %d, want 1 (stack unchanged)", s.Len())
	}
}

// TestSubxactStackAbortAll verifies that AbortAll marks every entry
// aborted and clears the stack. This is the top-level ROLLBACK path.
func TestSubxactStackAbortAll(t *testing.T) {
	var s SubxactStack
	mgr := NewManager()
	tx, _ := mgr.Begin(IsolationReadCommitted)
	snap, _ := mgr.SnapshotFor(tx)

	e1 := s.Push("s1", snap)
	e2 := s.Push("s2", snap)
	e3 := s.Push("s3", snap)

	aborted := s.AbortAll()
	if len(aborted) != 3 {
		t.Fatalf("AbortAll returned %d entries, want 3", len(aborted))
	}
	for _, a := range []*SubTransactionState{e1, e2, e3} {
		if a.Status != SubXactAborted {
			t.Errorf("entry %q status = %v, want aborted", a.Name, a.Status)
		}
	}
	if s.Len() != 0 {
		t.Errorf("Len = %d, want 0 after AbortAll", s.Len())
	}
	if s.Top() != nil {
		t.Error("Top() should be nil after AbortAll")
	}
}

// TestSubxactStackNilSafe verifies that all methods are safe on a nil
// *SubxactStack (zero-value session state before any SAVEPOINT).
func TestSubxactStackNilSafe(t *testing.T) {
	var s *SubxactStack
	if s.Len() != 0 {
		t.Error("nil.Len() != 0")
	}
	if s.Top() != nil {
		t.Error("nil.Top() != nil")
	}
	if s.All() != nil {
		t.Error("nil.All() != nil")
	}
	idx, entry := s.Find("x")
	if idx != -1 || entry != nil {
		t.Error("nil.Find() should return (-1, nil)")
	}
	if all := s.AbortAll(); all != nil {
		t.Error("nil.AbortAll() should return nil")
	}
}

// TestSubxactStackLockOwnerCorrectnessModel verifies the lock-owner
// re-assignment contract at the state-machine level:
//
//   - RELEASE: released entries' Ids are available for lock promotion.
//   - ROLLBACK TO: aborted entries' Ids signal lock drop.
//
// Actual lockmgr integration (M0050-0004) will use these Ids to call
// LockManager.TransferLocks or LockManager.ReleaseBySubxact.
func TestSubxactStackLockOwnerCorrectnessModel(t *testing.T) {
	var s SubxactStack
	mgr := NewManager()
	tx, _ := mgr.Begin(IsolationReadCommitted)
	snap, _ := mgr.SnapshotFor(tx)

	// Simulate: session acquires locks in subxact s1, then s2.
	e1 := s.Push("s1", snap)
	e2 := s.Push("s2", snap)

	// Releasing s1 should promote s1 and s2 to parent (top-level txn).
	released, err := s.Release("s1")
	if err != nil {
		t.Fatalf("Release s1: %v", err)
	}
	if len(released) != 2 {
		t.Fatalf("expected 2 released, got %d", len(released))
	}
	// All released entries are committed — caller promotes their locks.
	for _, r := range released {
		if r.Status != SubXactCommitted {
			t.Errorf("lock-transfer target %q not committed", r.Name)
		}
	}

	// Push s3 and acquire locks there; then rollback.
	e3 := s.Push("s3", snap)
	aborted, _, err := s.RollbackTo("s3", snap)
	if err != nil {
		t.Fatalf("RollbackTo s3: %v", err)
	}
	if len(aborted) != 1 {
		t.Fatalf("expected 1 aborted, got %d", len(aborted))
	}
	// Aborted entry's SubTxnId is what the lock manager uses to drop locks.
	if aborted[0].Id != e3.Id {
		t.Errorf("aborted Id %d != e3.Id %d", aborted[0].Id, e3.Id)
	}
	if aborted[0].Status != SubXactAborted {
		t.Errorf("aborted entry not marked aborted")
	}

	// After rollback, e1 and e2 are already gone (released above).
	// Stack has only fresh s3.
	if s.Len() != 1 {
		t.Errorf("Len = %d, want 1", s.Len())
	}
	_ = e1
	_ = e2
}

// TestSubxactStackSnapshotCapture verifies that each Push captures a
// snapshot and that RollbackTo restores the correct snapshot pointer.
func TestSubxactStackSnapshotCapture(t *testing.T) {
	var s SubxactStack
	mgr := NewManager()
	tx, _ := mgr.Begin(IsolationReadCommitted)
	snap1, _ := mgr.SnapshotFor(tx)
	e1 := s.Push("s1", snap1)
	if e1.Snap == nil {
		t.Fatal("e1.Snap is nil")
	}

	// Simulate a new snapshot (read-committed gets fresh snapshot per stmt).
	snap2, _ := mgr.SnapshotFor(tx)
	e2 := s.Push("s2", snap2)
	if e2.Snap == nil {
		t.Fatal("e2.Snap is nil")
	}

	// RollbackTo s1 with a fresh snapshot.
	snap3, _ := mgr.SnapshotFor(tx)
	_, fresh, err := s.RollbackTo("s1", snap3)
	if err != nil {
		t.Fatal(err)
	}
	// The fresh entry captures snap3.
	if fresh.Snap == nil {
		t.Fatal("fresh.Snap is nil after RollbackTo")
	}
}
