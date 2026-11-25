package xlog

import "testing"

// TestSlotsSetPhysicalXmins pins hot_standby_feedback on a physical slot
// (PhysicalReplicationSlotNewXmin, M0149-0077i): the reported xmin replaces
// the slot's (it may move backward), 0 clears it, MinXmin folds it into the
// horizon, and the value survives a reload of pg_replslot (slotdata.xmin).
func TestSlotsSetPhysicalXmins(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenSlots(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("s1", SlotPhysical, 0x1000); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("s2", SlotPhysical, 0x1000); err != nil {
		t.Fatal(err)
	}
	if got := s.MinXmin(); got != 0 {
		t.Fatalf("MinXmin with no feedback = %d", got)
	}
	if err := s.SetPhysicalXmins("s1", 900, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPhysicalXmins("s2", 750, 0); err != nil {
		t.Fatal(err)
	}
	if got := s.MinXmin(); got != 750 {
		t.Fatalf("MinXmin = %d, want 750", got)
	}
	if err := s.SetPhysicalXmins("s1", 800, 0); err != nil { // backward is allowed
		t.Fatal(err)
	}

	r, err := OpenSlots(dir)
	if err != nil {
		t.Fatal(err)
	}
	if sl, _ := r.Get("s1"); sl.Xmin != 800 {
		t.Fatalf("reloaded s1 xmin = %d, want 800", sl.Xmin)
	}
	if err := r.SetPhysicalXmins("s2", 0, 0); err != nil { // feedback off
		t.Fatal(err)
	}
	if got := r.MinXmin(); got != 800 {
		t.Fatalf("MinXmin after clearing s2 = %d, want 800", got)
	}
	if err := r.SetPhysicalXmins("nope", 1, 0); err != ErrSlotNotFound {
		t.Fatalf("unknown slot: %v", err)
	}
}
