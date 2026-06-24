package catalog

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestVisiblePartitionChildren pins the snapshot-relative omission of a
// concurrently-detached partition (design 0118-0058): a child stamped with a
// detach epoch is dropped for a statement whose snapshot epoch already observed
// the detach (snapshotEpoch >= e), and retained for an earlier snapshot
// (snapshotEpoch < e) or an undetached child (e == 0). This is the catalog-level
// rule that lets a READ COMMITTED statement stop seeing the partition the instant
// the detach is marked while a REPEATABLE READ transaction that began earlier
// keeps scanning it until commit.
func TestVisiblePartitionChildren(t *testing.T) {
	p1 := &Table{OID: 1, Name: "d_listp1"}
	p2 := &Table{OID: 2, Name: "d_listp2", DetachPendingEpoch: 5}

	t.Run("nil in nil out", func(t *testing.T) {
		if got := VisiblePartitionChildren(nil, 9); got != nil {
			t.Fatalf("expected nil, got %v", got)
		}
	})

	t.Run("no detach pending returns same backing slice", func(t *testing.T) {
		in := []*Table{p1}
		got := VisiblePartitionChildren(in, 100)
		if len(got) != 1 || got[0] != p1 {
			t.Fatalf("unexpected %v", names(got))
		}
	})

	t.Run("snapshot after detach drops the partition (READ COMMITTED)", func(t *testing.T) {
		in := []*Table{p1, p2}
		got := VisiblePartitionChildren(in, 5) // epoch == stamp ⇒ detach is visible
		if len(got) != 1 || got[0] != p1 {
			t.Fatalf("expected only d_listp1, got %v", names(got))
		}
		got = VisiblePartitionChildren(in, 6) // strictly newer snapshot
		if len(got) != 1 || got[0] != p1 {
			t.Fatalf("expected only d_listp1, got %v", names(got))
		}
	})

	t.Run("snapshot before detach keeps the partition (REPEATABLE READ)", func(t *testing.T) {
		in := []*Table{p1, p2}
		got := VisiblePartitionChildren(in, 4) // snapshot predates the detach
		if len(got) != 2 {
			t.Fatalf("expected both partitions, got %v", names(got))
		}
		got = VisiblePartitionChildren(in, 0) // snapshot captured before any detach epoch
		if len(got) != 2 {
			t.Fatalf("expected both partitions, got %v", names(got))
		}
	})
}

// TestPartitionDetachPendingLifecycle verifies the Mark/Clear API maintains an
// O(1) pending-detach count that the cross-session plan cache consults via
// HasPendingPartitionDetach (design 0118-0058).
func TestPartitionDetachPendingLifecycle(t *testing.T) {
	c := NewInMemory()
	parent, err := c.CreateTable(parser.ObjectName{Name: "d_listp"}, []Column{{Name: "a", Type: Type{Name: "int4"}}})
	if err != nil {
		t.Fatal(err)
	}
	child, err := c.CreateTable(parser.ObjectName{Name: "d_listp2"}, []Column{{Name: "a", Type: Type{Name: "int4"}}})
	if err != nil {
		t.Fatal(err)
	}
	c.RegisterPartitionChild(parent.OID, child.OID)

	if c.HasPendingPartitionDetach() {
		t.Fatal("no detach pending at rest")
	}

	if !c.MarkPartitionDetachPending(child.OID, 7) {
		t.Fatal("MarkPartitionDetachPending should find the registered child")
	}
	if !c.HasPendingPartitionDetach() {
		t.Fatal("detach must be pending after Mark")
	}
	if child.DetachPendingEpoch != 7 {
		t.Fatalf("child epoch = %d, want 7", child.DetachPendingEpoch)
	}

	// Re-stamping the same child must not double the count.
	c.MarkPartitionDetachPending(child.OID, 8)
	c.ClearPartitionDetachPending(child.OID)
	if c.HasPendingPartitionDetach() {
		t.Fatal("count must reach zero after a single Clear despite re-stamp")
	}
	if child.DetachPendingEpoch != 0 {
		t.Fatalf("child epoch = %d, want 0 after Clear", child.DetachPendingEpoch)
	}

	// Unknown child is a no-op.
	if c.MarkPartitionDetachPending(99999, 1) {
		t.Fatal("Mark on an unknown OID should report false")
	}
	if c.HasPendingPartitionDetach() {
		t.Fatal("unknown-OID Mark must not change the count")
	}
}
