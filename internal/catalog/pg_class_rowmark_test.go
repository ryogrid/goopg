package catalog

import "testing"

// TestPgClassRowMarks exercises the explicit pg_class tuple-lock store used by
// the intra-grant-inplace rowmark wait (design 0118-0113): record holders,
// surface them for the in-place updater's conflict wait, and clear on txn end.
func TestPgClassRowMarks(t *testing.T) {
	c := &InMemory{}

	const relA, relB = uint32(16400), uint32(16500)

	// No marks yet.
	if got := c.PgClassRowMarks(relA); got != nil {
		t.Fatalf("empty store: got %v, want nil", got)
	}

	// FOR KEY SHARE → non-conflicting; FOR NO KEY UPDATE → conflicting.
	c.AddPgClassRowMark(relA, 100, false) // keyshr
	c.AddPgClassRowMark(relA, 200, true)  // sfnku
	c.AddPgClassRowMark(relB, 300, true)  // different relation

	marks := c.PgClassRowMarks(relA)
	if len(marks) != 2 {
		t.Fatalf("relA holders: got %d, want 2 (%v)", len(marks), marks)
	}
	conflicts := map[uint32]bool{}
	for _, m := range marks {
		conflicts[m.XID] = m.ConflictsWithInplace
	}
	if conflicts[100] {
		t.Errorf("xid 100 (KEY SHARE) must not conflict")
	}
	if !conflicts[200] {
		t.Errorf("xid 200 (NO KEY UPDATE) must conflict")
	}

	// A later stronger acquisition by the same xid must not be downgraded by an
	// earlier weak mark (OR semantics).
	c.AddPgClassRowMark(relA, 100, true)
	for _, m := range c.PgClassRowMarks(relA) {
		if m.XID == 100 && !m.ConflictsWithInplace {
			t.Errorf("xid 100 upgrade to conflicting was lost")
		}
	}

	// Zero relOID / xid are ignored.
	c.AddPgClassRowMark(0, 999, true)
	c.AddPgClassRowMark(relA, 0, true)
	if got := c.PgClassRowMarks(0); got != nil {
		t.Errorf("relOID 0 must record nothing, got %v", got)
	}

	// Clearing a holder removes it from every relation; an emptied relation
	// drops out entirely.
	c.ClearPgClassRowMarksForXID(300)
	if got := c.PgClassRowMarks(relB); got != nil {
		t.Errorf("relB after clearing its only holder: got %v, want nil", got)
	}
	c.ClearPgClassRowMarksForXID(100)
	c.ClearPgClassRowMarksForXID(200)
	if got := c.PgClassRowMarks(relA); got != nil {
		t.Errorf("relA after clearing all holders: got %v, want nil", got)
	}
}
