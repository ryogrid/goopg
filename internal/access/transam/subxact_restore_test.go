package transam

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestScanParentsEmpty: a freshly-opened SLRU (no segments, or a bootstrap-zeroed
// 0000 segment) scans to an empty map — a fresh cluster restores nothing.
func TestScanParentsEmpty(t *testing.T) {
	dir := t.TempDir()
	slru, err := OpenSubtransSLRU(dir)
	if err != nil {
		t.Fatalf("OpenSubtransSLRU: %v", err)
	}
	got, err := slru.ScanParents()
	if err != nil {
		t.Fatalf("ScanParents (no segments): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ScanParents on empty SLRU = %v, want empty", got)
	}

	// Writing a below-normal XID (skipped by SetParent) leaves the dir empty too.
	if err := slru.SetParent(1, 2); err != nil {
		t.Fatalf("SetParent(1,2): %v", err)
	}
	got, err = slru.ScanParents()
	if err != nil {
		t.Fatalf("ScanParents: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ScanParents after below-normal write = %v, want empty", got)
	}
}

// TestScanParentsRoundTrip: links spanning more than one page and more than one
// segment round-trip exactly through the slot↔XID arithmetic.
func TestScanParentsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	slru, err := OpenSubtransSLRU(dir)
	if err != nil {
		t.Fatalf("OpenSubtransSLRU: %v", err)
	}

	// Chosen to exercise: page 0 of seg 0, a later page of seg 0 (xid >=
	// subtransXactsPerPage=2048), and seg 1 (xid >= subtransXactsPerSegment=65536).
	want := map[storage.TransactionID]storage.TransactionID{
		10:                              3,                            // seg 0, page 0
		subtransXactsPerPage + 5:        7,                            // seg 0, page 1
		subtransXactsPerPage*3 + 1:      11,                           // seg 0, page 3
		subtransXactsPerSegment + 42:    subtransXactsPerSegment + 40, // seg 1, page 0
		subtransXactsPerSegment*2 + 100: 99,                           // seg 2
		subtransXactsPerSegment + subtransXactsPerPage*10 + 7: 123, // seg 1, page 10
	}
	for sub, par := range want {
		if err := slru.SetParent(sub, par); err != nil {
			t.Fatalf("SetParent(%d,%d): %v", sub, par, err)
		}
	}

	got, err := slru.ScanParents()
	if err != nil {
		t.Fatalf("ScanParents: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("ScanParents returned %d links, want %d: %v", len(got), len(want), got)
	}
	for sub, par := range want {
		if got[sub] != par {
			t.Errorf("ScanParents[%d] = %d, want %d", sub, got[sub], par)
		}
	}

	// Cross-check against the single-slot GetParent reader for a couple of XIDs.
	for _, sub := range []storage.TransactionID{10, subtransXactsPerSegment + 42} {
		gp, err := slru.GetParent(sub)
		if err != nil {
			t.Fatalf("GetParent(%d): %v", sub, err)
		}
		if gp != want[sub] {
			t.Errorf("GetParent(%d) = %d, want %d (must agree with ScanParents)", sub, gp, want[sub])
		}
	}
}

// TestRestoreFromSLRUWithoutPersistence: calling RestoreFromSLRU before
// EnablePersistence fails loudly rather than silently restoring nothing.
func TestRestoreFromSLRUWithoutPersistence(t *testing.T) {
	m := NewSubxactMap()
	if _, err := m.RestoreFromSLRU(); err == nil {
		t.Fatal("RestoreFromSLRU without EnablePersistence must error")
	}
}

// TestSubxactMapRestoreAcrossRestart: a SubxactMap that persisted parent links is
// faithfully reconstructed by a fresh SubxactMap pointed at the same directory —
// the defining "survives a restart" property (gap G5 read path).
func TestSubxactMapRestoreAcrossRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pg_subtrans")

	// Session 1: register a two-level chain (100 → 50 → 10-top) plus a sibling.
	a := NewSubxactMap()
	if err := a.EnablePersistence(dir); err != nil {
		t.Fatalf("EnablePersistence (A): %v", err)
	}
	a.Register(100, 50)
	a.Register(50, 10)
	a.Register(200, 10)

	// Session 2 (restart): a fresh map restores from disk.
	b := NewSubxactMap()
	if err := b.EnablePersistence(dir); err != nil {
		t.Fatalf("EnablePersistence (B): %v", err)
	}
	n, err := b.RestoreFromSLRU()
	if err != nil {
		t.Fatalf("RestoreFromSLRU (B): %v", err)
	}
	if n != 3 {
		t.Fatalf("RestoreFromSLRU restored %d links, want 3", n)
	}
	if got := b.Parent(100); got != 50 {
		t.Errorf("Parent(100) after restore = %d, want 50", got)
	}
	if got := b.TopLevelXid(100); got != 10 {
		t.Errorf("TopLevelXid(100) after restore = %d, want 10 (chain 100→50→10)", got)
	}
	if !b.IsSubxact(200) {
		t.Errorf("IsSubxact(200) after restore = false, want true")
	}
	if b.IsSubxact(10) {
		t.Errorf("IsSubxact(10) after restore = true, want false (10 is top-level)")
	}
}

// TestManagerSubxactMapAttachment: with a persistent SubxactMap attached, the
// Manager persists parentage on RegisterSubXid and a fresh Manager + restored map
// resolves it; with no map attached the Manager is byte-identical to v0.
func TestManagerSubxactMapAttachment(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pg_subtrans")

	// Manager 1: attach a persistent map, register a chain.
	m1 := NewManager()
	sm1 := NewSubxactMap()
	if err := sm1.EnablePersistence(dir); err != nil {
		t.Fatalf("EnablePersistence (m1): %v", err)
	}
	m1.SetSubxactMap(sm1)
	m1.RegisterSubXid(100, 50)
	m1.RegisterSubXid(50, 10)
	m1.MarkSubxactAborted(100)

	if got := m1.TopLevelXid(100); got != 10 {
		t.Errorf("m1.TopLevelXid(100) = %d, want 10", got)
	}
	if !m1.IsAborted(100) {
		t.Error("m1.IsAborted(100) = false, want true")
	}
	if !m1.IsSubxact(50) {
		t.Error("m1.IsSubxact(50) = false, want true")
	}

	// Manager 2 (restart): fresh Manager + restored map resolves the persisted
	// parent chain. (Abort status is not persisted — only parent links are.)
	m2 := NewManager()
	sm2 := NewSubxactMap()
	if err := sm2.EnablePersistence(dir); err != nil {
		t.Fatalf("EnablePersistence (m2): %v", err)
	}
	if _, err := sm2.RestoreFromSLRU(); err != nil {
		t.Fatalf("RestoreFromSLRU (m2): %v", err)
	}
	m2.SetSubxactMap(sm2)
	if got := m2.TopLevelXid(100); got != 10 {
		t.Errorf("m2.TopLevelXid(100) after restart = %d, want 10", got)
	}
	if !m2.IsSubxact(100) {
		t.Error("m2.IsSubxact(100) after restart = false, want true")
	}

	// No map attached: pure in-memory v0 path is unchanged.
	m3 := NewManager()
	m3.RegisterSubXid(7, 3)
	if got := m3.TopLevelXid(7); got != 3 {
		t.Errorf("m3.TopLevelXid(7) [no map] = %d, want 3", got)
	}
	if m3.IsSubxact(999) {
		t.Error("m3.IsSubxact(999) [no map] = true, want false")
	}
}
