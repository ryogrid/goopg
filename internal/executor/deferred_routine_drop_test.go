package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestDeferredRoutineDropSession verifies the BasicSession deferral list for
// DROP FUNCTION (M0118-0009 `stats`): Add/Take round-trip, the savepoint-depth
// cancel so ROLLBACK TO before a deferred drop does not remove the function at
// the outer COMMIT, the DROP-then-CREATE-same-signature match (applied early so
// a recreate proceeds), and that EndExplicitTransaction clears the list.
func TestDeferredRoutineDropSession(t *testing.T) {
	s := NewBasicSession()

	rA := &catalog.Routine{OID: 100, Schema: "public", Name: "f", ArgTypes: nil}
	rB := &catalog.Routine{OID: 300, Schema: "public", Name: "g", ArgTypes: nil}
	s.AddDeferredRoutineDrop(DeferredRoutineDrop{Routine: rA, SavepointDepth: 0})
	s.AddDeferredRoutineDrop(DeferredRoutineDrop{Routine: rB, SavepointDepth: 2})

	// ROLLBACK TO a savepoint at depth 2 discards exactly the depth>=2 entry.
	s.CancelDeferredRoutineDropsToDepth(2)
	got := s.TakeDeferredRoutineDrops()
	if len(got) != 1 || got[0].Routine == nil || got[0].Routine.OID != 100 {
		t.Fatalf("after cancel, Take = %+v, want the depth-0 drop of OID 100", got)
	}
	if more := s.TakeDeferredRoutineDrops(); len(more) != 0 {
		t.Fatalf("second Take returned %d entries, want 0", len(more))
	}

	// DROP-then-CREATE same signature: TakeDeferredRoutineDropMatching returns and
	// removes the matching entry (so CREATE can apply the drop early), and leaves
	// non-matching entries intact. The bare-name routine f has signature "()".
	s.AddDeferredRoutineDrop(DeferredRoutineDrop{Routine: rA, SavepointDepth: 0})
	s.AddDeferredRoutineDrop(DeferredRoutineDrop{Routine: rB, SavepointDepth: 0})
	if m := s.TakeDeferredRoutineDropMatching("public", "f", rA.Signature()); m == nil || m.OID != 100 {
		t.Fatalf("match on (public, f, %q) = %+v, want OID 100", rA.Signature(), m)
	}
	// A non-matching signature returns nil and leaves the list unchanged.
	if m := s.TakeDeferredRoutineDropMatching("public", "f", "(int)"); m != nil {
		t.Fatalf("unexpected match for a different signature: %+v", m)
	}
	// Empty schema is treated as public.
	if m := s.TakeDeferredRoutineDropMatching("", "g", rB.Signature()); m == nil || m.OID != 300 {
		t.Fatalf("empty-schema match on g = %+v, want OID 300 (treated as public)", m)
	}
	if rem := s.TakeDeferredRoutineDrops(); len(rem) != 0 {
		t.Fatalf("after both matches, %d entries remain, want 0", len(rem))
	}

	// EndExplicitTransaction clears any leftover deferred drops.
	s.AddDeferredRoutineDrop(DeferredRoutineDrop{Routine: rA, SavepointDepth: 0})
	s.EndExplicitTransaction()
	if rem := s.TakeDeferredRoutineDrops(); len(rem) != 0 {
		t.Fatalf("EndExplicitTransaction left %d deferred drops, want 0", len(rem))
	}
}
