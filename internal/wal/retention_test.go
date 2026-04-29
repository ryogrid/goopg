package wal

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestRemoveOldSegmentsKeepsContainingSegment confirms the writer
// preserves the segment that contains the keep-LSN itself: a
// caller passing the LSN of the latest checkpoint marker must not
// have that marker's WAL deleted out from under recovery.
func TestRemoveOldSegmentsKeepsContainingSegment(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 32})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// Write enough payload to span four segments. recordHeader is 8
	// bytes, so a 24-byte payload makes one record exactly fill one
	// segment (32-byte segment / 8+24).
	for i := 0; i < 4; i++ {
		payload := make([]byte, 24)
		payload[0] = byte('a' + i)
		if _, _, err := w.Append(payload); err != nil {
			t.Fatal(err)
		}
	}

	// Pre-condition: four segments exist.
	if names := segmentNames(t, walDir); len(names) != 4 {
		t.Fatalf("pre-recycle segment count = %d (%v), want 4", len(names), names)
	}

	// Keep the LSN at the very first byte of the third segment
	// (segNo=2, byte offset 0 inside the segment, 1-based LSN =
	// 2*32 + 1 = 65). Segments 0 and 1 are obsolete; 2 and 3 stay.
	keepLSN := uint64(2*32 + 1)
	removed, err := w.RemoveOldSegments(keepLSN)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want 2", removed)
	}
	got := segmentNames(t, walDir)
	want := []string{
		formatSegmentName(2),
		formatSegmentName(3),
	}
	if !equalStringSlices(got, want) {
		t.Fatalf("post-recycle segments = %v, want %v", got, want)
	}
}

// TestRemoveOldSegmentsZeroLSNIsNoop pins the entry-point guard so a
// caller that hasn't observed any LSN yet (fresh process, no slots,
// no checkpoint) doesn't accidentally drop everything in pg_wal.
func TestRemoveOldSegmentsZeroLSNIsNoop(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 32})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, _, err := w.Append(make([]byte, 24)); err != nil {
		t.Fatal(err)
	}
	removed, err := w.RemoveOldSegments(0)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Fatalf("removed = %d, want 0", removed)
	}
	if names := segmentNames(t, walDir); len(names) != 1 {
		t.Fatalf("post-noop segment count = %d, want 1", len(names))
	}
}

// TestRemoveOldSegmentsClosesCachedHandle ensures the writer drops
// its open file descriptor for a removed segment. Without the
// delete(s.files, segNo) cleanup, a subsequent op that reused that
// segment number would write to the unlinked inode.
func TestRemoveOldSegmentsClosesCachedHandle(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 32})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for i := 0; i < 3; i++ {
		if _, _, err := w.Append(make([]byte, 24)); err != nil {
			t.Fatal(err)
		}
	}
	// Drop segments 0 and 1 (keep segment 2).
	if _, err := w.RemoveOldSegments(uint64(2*32 + 1)); err != nil {
		t.Fatal(err)
	}
	// Append more — the writer should open segment 3 fresh.
	for i := 0; i < 2; i++ {
		if _, _, err := w.Append(make([]byte, 24)); err != nil {
			t.Fatal(err)
		}
	}
	// Segments 0 and 1 must remain unlinked.
	for _, name := range []string{formatSegmentName(0), formatSegmentName(1)} {
		if _, err := os.Stat(filepath.Join(walDir, name)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("segment %s reappeared (err=%v)", name, err)
		}
	}
}

// TestSlotsInvalidateLagging covers the per-checkpoint pruning
// decision: only slots whose lag exceeds the bound flip; the rest
// keep pinning WAL.
func TestSlotsInvalidateLagging(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenSlots(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("near", SlotPhysical, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("far", SlotPhysical, 10); err != nil {
		t.Fatal(err)
	}
	// currentLSN = 200. Bound = 50.
	// near: lag = 200 - 100 = 100 > 50 → invalidate.
	// far:  lag = 200 - 10  = 190 > 50 → invalidate.
	invalidated, err := s.InvalidateLagging(200, 50)
	if err != nil {
		t.Fatal(err)
	}
	if !equalStringSlices(invalidated, []string{"far", "near"}) {
		t.Fatalf("invalidated = %v, want [far near]", invalidated)
	}

	// MinRestartLSN now ignores both slots → returns 0 (unbounded
	// retention from the slot side).
	if got := s.MinRestartLSN(); got != 0 {
		t.Fatalf("MinRestartLSN after invalidation = %d, want 0", got)
	}

	// Persistence: invalidated bit must round-trip through OpenSlots.
	s2, err := OpenSlots(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"near", "far"} {
		got, err := s2.Get(name)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Invalidated {
			t.Fatalf("slot %q not invalidated after reload", name)
		}
	}
}

// TestSlotsInvalidateLaggingHonoursBoundary confirms the lag
// comparison is strict: a slot whose lag exactly equals the bound is
// preserved (matches upstream's `> max_slot_wal_keep_size` check, not
// `>=`).
func TestSlotsInvalidateLaggingHonoursBoundary(t *testing.T) {
	s, err := OpenSlots(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("borderline", SlotPhysical, 100); err != nil {
		t.Fatal(err)
	}
	// lag = 50, bound = 50 → keep.
	invalidated, err := s.InvalidateLagging(150, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(invalidated) != 0 {
		t.Fatalf("invalidated = %v, want none (lag exactly equals bound)", invalidated)
	}
	// lag = 51 → flip.
	invalidated, err = s.InvalidateLagging(151, 50)
	if err != nil {
		t.Fatal(err)
	}
	if !equalStringSlices(invalidated, []string{"borderline"}) {
		t.Fatalf("invalidated = %v, want [borderline]", invalidated)
	}
}

// TestSlotsInvalidateLaggingDisabled confirms the
// max_slot_wal_keep_size = -1 sentinel really does disable
// invalidation: a slot lagged by an absurd amount stays alive.
func TestSlotsInvalidateLaggingDisabled(t *testing.T) {
	s, err := OpenSlots(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("ancient", SlotPhysical, 1); err != nil {
		t.Fatal(err)
	}
	for _, bound := range []int64{0, -1, -1024} {
		invalidated, err := s.InvalidateLagging(1<<30, bound)
		if err != nil {
			t.Fatalf("bound=%d: %v", bound, err)
		}
		if len(invalidated) != 0 {
			t.Fatalf("bound=%d: invalidated=%v, want none", bound, invalidated)
		}
	}
}

// TestSlotAwareRetainerPrunesBelowSlotHorizon is the integration test
// that ties the three pieces together: a checkpointer-style call into
// the retainer must keep WAL the active slot still needs and remove
// everything older.
func TestSlotAwareRetainerPrunesBelowSlotHorizon(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 32})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for i := 0; i < 6; i++ {
		if _, _, err := w.Append(make([]byte, 24)); err != nil {
			t.Fatal(err)
		}
	}

	slots, err := OpenSlots(filepath.Dir(walDir))
	if err != nil {
		t.Fatal(err)
	}
	// Slot anchored at the start of segment 1 (LSN 33). Even
	// though the checkpoint marker says "everything below segment
	// 5 is redo-redundant", retention must NOT delete segment 1
	// because the slot still needs it.
	if _, err := slots.Create("standby", SlotPhysical, 33); err != nil {
		t.Fatal(err)
	}

	r := &SlotAwareRetainer{
		Writer:           w,
		Slots:            slots,
		MaxSlotKeepBytes: 0, // unlimited — slot keeps WAL forever
	}
	// "Checkpoint" sits inside segment 5.
	if err := r.Retain(uint64(5*32 + 1)); err != nil {
		t.Fatal(err)
	}
	// Segment 0 is obsolete (slot starts at segment 1). Segments
	// 1..5 must remain.
	got := segmentNames(t, walDir)
	want := []string{
		formatSegmentName(1),
		formatSegmentName(2),
		formatSegmentName(3),
		formatSegmentName(4),
		formatSegmentName(5),
	}
	if !equalStringSlices(got, want) {
		t.Fatalf("segments after retain = %v, want %v", got, want)
	}
}

// TestSlotAwareRetainerInvalidatesAndPrunes confirms that when
// max_slot_wal_keep_size kicks in the retainer flips the slot AND
// then unlinks the segments the now-invalidated slot was holding.
func TestSlotAwareRetainerInvalidatesAndPrunes(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 32})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for i := 0; i < 6; i++ {
		if _, _, err := w.Append(make([]byte, 24)); err != nil {
			t.Fatal(err)
		}
	}

	slots, err := OpenSlots(filepath.Dir(walDir))
	if err != nil {
		t.Fatal(err)
	}
	// Slot still anchored way back at LSN 1 (start of segment 0).
	// Lag = WrittenLSN - 1 ≈ 6*32. With a 32-byte bound the slot
	// is far past the cap and must be invalidated.
	if _, err := slots.Create("laggy", SlotPhysical, 1); err != nil {
		t.Fatal(err)
	}
	r := &SlotAwareRetainer{
		Writer:           w,
		Slots:            slots,
		MaxSlotKeepBytes: 32,
	}
	if err := r.Retain(uint64(5*32 + 1)); err != nil {
		t.Fatal(err)
	}
	got, err := slots.Get("laggy")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Invalidated {
		t.Fatalf("slot not invalidated; want invalidated after retain")
	}
	// With the slot out of the way, retention falls back to the
	// checkpoint horizon: segments 0..4 are obsolete, segment 5
	// (containing the marker) stays.
	if names := segmentNames(t, walDir); !equalStringSlices(names, []string{formatSegmentName(5)}) {
		t.Fatalf("segments after retain = %v, want [%s]", names, formatSegmentName(5))
	}
}

// segmentNames returns the sorted list of WAL segment file names in
// dir. Anything that doesn't parse as a 24-hex-digit segment name is
// skipped (e.g. tempfiles, subdirs).
func segmentNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if _, ok := parseSegmentName(e.Name()); !ok {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
