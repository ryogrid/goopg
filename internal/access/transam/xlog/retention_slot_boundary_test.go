package xlog

import (
	"path/filepath"
	"testing"
)

// TestRetainSlotOnSegmentBoundaryReleasesPreviousSegment pins KeepLogSeg's
// XLByteToSeg(restart_lsn) for a slot whose restart_lsn is exactly a segment
// boundary — what pg_replication_slot_advance to pg_current_wal_lsn() after
// pg_switch_wal leaves (001_stream_rep, M0149-0077j). A slot LSN is a PG byte
// position, so 3*segSize is the first byte of segment 3: segments 0-2 are
// obsolete. Treating it as the writer's 1-based LSN kept segment 2 as well.
func TestRetainSlotOnSegmentBoundaryReleasesPreviousSegment(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	const segSize = retentionTestSegSize
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: segSize})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	appendThroughSegment(t, w, segSize, 5)

	slots, err := OpenSlots(filepath.Dir(walDir))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := slots.Create("phys", SlotPhysical, uint64(segSize+1)); err != nil {
		t.Fatal(err)
	}
	if _, err := slots.AdvancePhysical("phys", uint64(3*segSize)); err != nil {
		t.Fatal(err)
	}
	r := &SlotAwareRetainer{Writer: w, Slots: slots}
	if err := r.Retain(uint64(5*segSize + 1)); err != nil {
		t.Fatal(err)
	}
	got := segmentNames(t, walDir)
	if len(got) == 0 || got[0] != formatSegmentName(3) {
		t.Fatalf("segments after retention = %v, want the oldest to be %s", got, formatSegmentName(3))
	}
}

// TestSlotsAdvancePhysical pins pg_replication_slot_advance's checks on a
// physical slot (slotfuncs.c): forward moves persist, an equal target is a
// no-op, a backward target errors with the minimum, an active slot is in
// use, and a logical slot is refused.
func TestSlotsAdvancePhysical(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenSlots(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("p", SlotPhysical, 0x1000); err != nil {
		t.Fatal(err)
	}
	if end, err := s.AdvancePhysical("p", 0x2000); err != nil || end != 0x2000 {
		t.Fatalf("advance = %x, %v", end, err)
	}
	if end, err := s.AdvancePhysical("p", 0x2000); err != nil || end != 0x2000 {
		t.Fatalf("equal advance = %x, %v", end, err)
	}
	var below *SlotAdvanceBelowMinimumError
	if _, err := s.AdvancePhysical("p", 0x1800); err == nil {
		t.Fatal("backward advance succeeded")
	} else if b, ok := err.(*SlotAdvanceBelowMinimumError); !ok || b.Minimum != 0x2000 {
		t.Fatalf("backward advance error = %v (%T)", err, below)
	}
	r, err := OpenSlots(dir)
	if err != nil {
		t.Fatal(err)
	}
	if sl, _ := r.Get("p"); sl.RestartLSN != 0x2000 {
		t.Fatalf("reloaded restart_lsn = %x, want 2000", sl.RestartLSN)
	}
	if err := r.SetActive("p", true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AdvancePhysical("p", 0x3000); err != ErrSlotInUse {
		t.Fatalf("active slot: %v", err)
	}
	if _, err := r.CreateLogical("l", "pgoutput", "postgres", 0x1000); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AdvancePhysical("l", 0x3000); err != ErrSlotKindMismatch {
		t.Fatalf("logical slot: %v", err)
	}
}
