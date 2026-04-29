package wal

import (
	"errors"
	"path/filepath"
	"testing"
)

// TestSlotsCreateGetList pins the basic CRUD shape: creating a slot
// returns the snapshot, Get retrieves it, and List orders by name.
func TestSlotsCreateGetList(t *testing.T) {
	s, err := OpenSlots(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("primary", SlotPhysical, 0x100); err != nil {
		t.Fatalf("Create primary: %v", err)
	}
	if _, err := s.Create("backup", SlotPhysical, 0x200); err != nil {
		t.Fatalf("Create backup: %v", err)
	}
	got, err := s.Get("primary")
	if err != nil {
		t.Fatalf("Get primary: %v", err)
	}
	if got.RestartLSN != 0x100 || got.ConfirmedFlushLSN != 0x100 {
		t.Errorf("primary LSNs = (%x, %x), want (0x100, 0x100)",
			got.RestartLSN, got.ConfirmedFlushLSN)
	}
	list := s.List()
	if len(list) != 2 || list[0].Name != "backup" || list[1].Name != "primary" {
		t.Errorf("List = %+v, want [backup, primary]", list)
	}
}

// TestSlotsDuplicateRejected: a second Create with the same name must
// fail with ErrSlotExists, not silently overwrite the original.
func TestSlotsDuplicateRejected(t *testing.T) {
	s, _ := OpenSlots(t.TempDir())
	if _, err := s.Create("primary", SlotPhysical, 0x100); err != nil {
		t.Fatal(err)
	}
	_, err := s.Create("primary", SlotPhysical, 0x200)
	if !errors.Is(err, ErrSlotExists) {
		t.Errorf("dup Create err = %v, want ErrSlotExists", err)
	}
}

// TestSlotsInvalidName: upstream slot-name rules restrict to
// [a-z0-9_]{1,63}. Reject anything else.
func TestSlotsInvalidName(t *testing.T) {
	s, _ := OpenSlots(t.TempDir())
	cases := []string{"", "Has-Dash", "UPPER", "with space", "ümlaut"}
	for _, name := range cases {
		if _, err := s.Create(name, SlotPhysical, 0); !errors.Is(err, ErrSlotInvalidName) {
			t.Errorf("Create(%q) err = %v, want ErrSlotInvalidName", name, err)
		}
	}
}

// TestSlotsDropAndActiveGuard: an active slot can't be dropped (would
// pull WAL out from under a connected walsender); inactive drops
// succeed.
func TestSlotsDropAndActiveGuard(t *testing.T) {
	s, _ := OpenSlots(t.TempDir())
	if _, err := s.Create("primary", SlotPhysical, 0x100); err != nil {
		t.Fatal(err)
	}
	if err := s.SetActive("primary", true); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	if err := s.Drop("primary"); !errors.Is(err, ErrSlotInUse) {
		t.Errorf("Drop active err = %v, want ErrSlotInUse", err)
	}
	if err := s.SetActive("primary", false); err != nil {
		t.Fatalf("SetActive false: %v", err)
	}
	if err := s.Drop("primary"); err != nil {
		t.Errorf("Drop inactive err = %v, want nil", err)
	}
	if _, err := s.Get("primary"); !errors.Is(err, ErrSlotNotFound) {
		t.Errorf("post-drop Get err = %v, want ErrSlotNotFound", err)
	}
}

// TestSlotsAdvanceLSN: AdvanceConfirmedFlushLSN moves both
// ConfirmedFlushLSN and RestartLSN forward but never backward.
// MinRestartLSN reflects the minimum across the registry.
func TestSlotsAdvanceLSN(t *testing.T) {
	s, _ := OpenSlots(t.TempDir())
	_, _ = s.Create("a", SlotPhysical, 0x100)
	_, _ = s.Create("b", SlotPhysical, 0x200)
	if err := s.AdvanceConfirmedFlushLSN("a", 0x150); err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceConfirmedFlushLSN("a", 0x140); err != nil {
		t.Fatal(err) // no-op, but non-error
	}
	got, _ := s.Get("a")
	if got.ConfirmedFlushLSN != 0x150 || got.RestartLSN != 0x150 {
		t.Errorf("after advance, a = (%x, %x), want (0x150, 0x150)",
			got.ConfirmedFlushLSN, got.RestartLSN)
	}
	if min := s.MinRestartLSN(); min != 0x150 {
		t.Errorf("MinRestartLSN = %x, want 0x150 (a's value)", min)
	}
}

// TestSlotsPersistence: after Create, OpenSlots on the same dir
// recovers the slot, validating the on-disk JSON layout.
func TestSlotsPersistence(t *testing.T) {
	dir := t.TempDir()
	s1, err := OpenSlots(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s1.Create("primary", SlotPhysical, 0xCAFEBABE); err != nil {
		t.Fatal(err)
	}
	if err := s1.AdvanceConfirmedFlushLSN("primary", 0xDEADBEEF); err != nil {
		t.Fatal(err)
	}
	// File layout: <dir>/pg_replslot/primary/state
	statePath := filepath.Join(dir, "pg_replslot", "primary", "state")
	if _, err := readSlotFile(filepath.Dir(statePath)); err != nil {
		t.Fatalf("on-disk slot: %v", err)
	}
	s2, err := OpenSlots(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s2.Get("primary")
	if err != nil {
		t.Fatal(err)
	}
	if got.ConfirmedFlushLSN != 0xDEADBEEF || got.RestartLSN != 0xDEADBEEF {
		t.Errorf("rehydrated primary = %+v, want LSNs 0xDEADBEEF", got)
	}
	// Active is in-memory only; must not have persisted.
	if got.Active {
		t.Errorf("rehydrated primary.Active = true, want false")
	}
}
