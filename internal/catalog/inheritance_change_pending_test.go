package catalog

import "testing"

// TestInheritanceChangePendingCounter verifies the deferred-inheritance-change
// counter that gates the cross-session plan-cache bypass (alter-table-4,
// design 0118-0080): it is zero in the steady state, tracks Mark/Unmark, and is
// clamped at zero so an extra Unmark cannot drive it negative.
func TestInheritanceChangePendingCounter(t *testing.T) {
	c := NewInMemory()
	if c.HasPendingInheritanceChange() {
		t.Fatalf("fresh catalog reports a pending inheritance change")
	}
	c.MarkInheritanceChangePending()
	c.MarkInheritanceChangePending()
	if !c.HasPendingInheritanceChange() {
		t.Fatalf("after two Marks, expected pending=true")
	}
	c.UnmarkInheritanceChangePending()
	if !c.HasPendingInheritanceChange() {
		t.Fatalf("after one of two Unmarks, expected pending still true")
	}
	c.UnmarkInheritanceChangePending()
	if c.HasPendingInheritanceChange() {
		t.Fatalf("after balancing Unmarks, expected pending=false")
	}
	// Clamp: an extra Unmark must not underflow to a perpetually-true state.
	c.UnmarkInheritanceChangePending()
	if c.HasPendingInheritanceChange() {
		t.Fatalf("extra Unmark drove the counter negative")
	}
	c.MarkInheritanceChangePending()
	if !c.HasPendingInheritanceChange() {
		t.Fatalf("Mark after clamp did not re-arm the pending flag")
	}
}
