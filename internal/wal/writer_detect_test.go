package wal

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDetectWritePos_NonZeroFirstSeg is the reproducer for the
// run-007 crash-restart bug: hard-killing goopg after retention
// removed segment 0 left the cluster in an un-restartable state.
//
// The symptom was:
//   goopg start: goopg: wal: wal: first segment is
//   00000000000000000000023F, expected 000000000000000000000000
//
// M0045-0001 fixes detectWritePos to accept any firstSegNo and
// compute the correct absolute writePos = firstSegNo*segSize + ...
func TestDetectWritePos_NonZeroFirstSeg(t *testing.T) {
	const segSize = 1024

	// firstSegNo mirrors the run-007 reproducer.
	const firstSegNo = uint64(0x23F)

	walDir := t.TempDir()

	// Create two full segments (non-last) + one partial last segment.
	// Non-last segments must be exactly segSize bytes.
	writeSegFile := func(segNo uint64, size int) {
		t.Helper()
		name := filepath.Join(walDir, formatSegmentName(segNo))
		if err := os.WriteFile(name, make([]byte, size), 0o644); err != nil {
			t.Fatalf("writeSegFile %s: %v", formatSegmentName(segNo), err)
		}
	}

	writeSegFile(firstSegNo+0, segSize)   // 0x23F — full, non-last
	writeSegFile(firstSegNo+1, segSize)   // 0x240 — full, non-last
	writeSegFile(firstSegNo+2, 0)         // 0x241 — empty last segment (usedBytes=0)

	// Before M0045-0001 this returned:
	//   "wal: first segment is 00000000000000000000023F, expected 000000000000000000000000"
	writePos, _, err := detectWritePos(walDir, segSize, false)
	if err != nil {
		t.Fatalf("detectWritePos: %v", err)
	}

	// Expected: firstSegNo*segSize + segSize (0x23F) + segSize (0x240) + 0 (0x241)
	//         = (firstSegNo + 2) * segSize = 0x241 * segSize
	wantPos := int64(firstSegNo+2) * segSize
	if writePos != wantPos {
		t.Fatalf("writePos = %d, want %d (firstSegNo=0x%X, segSize=%d)",
			writePos, wantPos, firstSegNo, segSize)
	}
}

// TestDetectWritePos_ZeroFirstSegStillWorks verifies that the
// normal case (retention has not yet run, first segment is 0)
// continues to produce the same result after the M0045-0001 fix.
func TestDetectWritePos_ZeroFirstSegStillWorks(t *testing.T) {
	const segSize = 512

	walDir := t.TempDir()

	// Segments 0, 1, 2.
	writeSegFile := func(segNo uint64, size int) {
		name := filepath.Join(walDir, formatSegmentName(segNo))
		if err := os.WriteFile(name, make([]byte, size), 0o644); err != nil {
			t.Fatalf("writeSegFile: %v", err)
		}
	}
	writeSegFile(0, segSize) // full
	writeSegFile(1, segSize) // full
	writeSegFile(2, 0)       // empty last

	writePos, _, err := detectWritePos(walDir, segSize, false)
	if err != nil {
		t.Fatalf("detectWritePos: %v", err)
	}
	// Expected: 0 + segSize + segSize + 0 = 2 * segSize
	wantPos := int64(2) * segSize
	if writePos != wantPos {
		t.Fatalf("writePos = %d, want %d", writePos, wantPos)
	}
}

// TestDetectWritePos_GapDetectionAfterNonZeroStart verifies that gap
// detection (segments 0x23F and 0x241 with 0x240 missing) still
// returns an error after the M0045-0001 fix.
func TestDetectWritePos_GapDetectionAfterNonZeroStart(t *testing.T) {
	const segSize = 512
	const firstSegNo = uint64(0x23F)

	walDir := t.TempDir()

	// Write 0x23F and 0x241, skip 0x240 — this is a genuine gap.
	name1 := filepath.Join(walDir, formatSegmentName(firstSegNo))
	name2 := filepath.Join(walDir, formatSegmentName(firstSegNo+2))
	if err := os.WriteFile(name1, make([]byte, segSize), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name2, make([]byte, 0), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err := detectWritePos(walDir, segSize, false)
	if err == nil {
		t.Fatal("expected gap error, got nil")
	}
}

// TestDetectWritePos_SingleNonZeroSegment covers the single-segment
// case where retention removed all but the latest segment.
func TestDetectWritePos_SingleNonZeroSegment(t *testing.T) {
	const segSize = 256
	const firstSegNo = uint64(0x100)

	walDir := t.TempDir()

	// Only one segment at 0x100, with 64 bytes of content.
	name := filepath.Join(walDir, formatSegmentName(firstSegNo))
	if err := os.WriteFile(name, make([]byte, 64), 0o644); err != nil {
		t.Fatal(err)
	}

	writePos, _, err := detectWritePos(walDir, segSize, false)
	if err != nil {
		t.Fatalf("detectWritePos: %v", err)
	}
	// Expected: 0x100 * 256 + usedBytes
	// The 64-byte file is all zeros, so scanLastSegmentEnd returns
	// usedBytes=0 (EOS sentinel at byte 0).
	wantBase := int64(firstSegNo) * segSize
	if writePos != wantBase {
		t.Fatalf("writePos = %d, want %d (base for 0x100)", writePos, wantBase)
	}
}

// TestDetectWritePos_IgnoresEagerPhantomFutureSegment is the
// regression pin for the M0007 eager-next-segment-lookahead follow-up:
// state.eagerPreallocSegment can leave a full-size, entirely
// zero-filled segment 1 on disk beyond segment 0 (which itself is
// only partially written — the writer never got around to rolling
// over into segment 1 for real before this snapshot / a crash). A
// reopen must find the true end of WAL inside segment 0, not
// mistakenly trust it as "fully used" because a same-size segment 1
// exists above it.
func TestDetectWritePos_IgnoresEagerPhantomFutureSegment(t *testing.T) {
	const segSize = int64(1024)
	walDir := t.TempDir()

	// Build segment 0 for real, via the writer, so it contains
	// genuine decodable records followed by a real zero-fill tail —
	// exactly what a genuinely partially-written, preallocated
	// segment looks like on disk.
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: segSize, Preallocate: true})
	if err != nil {
		t.Fatal(err)
	}
	var lastEnd uint64
	for _, p := range []string{"alpha", "beta", "gamma"} {
		_, end, err := w.Append([]byte(p))
		if err != nil {
			t.Fatal(err)
		}
		lastEnd = end
	}
	if err := w.FlushUpTo(lastEnd); err != nil {
		t.Fatal(err)
	}
	// Wait for the writer's own eager lookahead to finish creating
	// segment 1, then close — simulating a crash right after eager
	// preallocation completed but before the writer ever rolled over
	// into segment 1 for real.
	w.stateRef.eagerWG.Wait()
	if _, err := os.Stat(filepath.Join(walDir, formatSegmentName(1))); err != nil {
		t.Fatalf("expected eager lookahead to have created segment 1: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	writePos, _, err := detectWritePos(walDir, segSize, false)
	if err != nil {
		t.Fatalf("detectWritePos: %v", err)
	}
	if writePos != int64(lastEnd) {
		t.Fatalf("writePos = %d, want %d (segment 0's real end — segment 1 is an untouched eager phantom and must be ignored)", writePos, lastEnd)
	}
}

// TestClose_WaitsForEagerJobTriggeredByItsOwnFlush is the regression
// pin for a review finding on the eager-lookahead follow-up: with
// Config.WALBuffers > 0, Append only touches the in-memory buffer —
// nothing reaches openSegment until something drains it. If nothing
// drains the buffer before Close, close()'s own flushUpTo call can be
// the FIRST caller of openSegment for one or more segments, and
// openSegment unconditionally kicks off a brand-new eager job for the
// segment after whichever one it just opened. A Wait() that only ran
// at the very top of close() (before flushUpTo) would return before
// any of these jobs even started, letting Close() return while a
// background goroutine is still writing into the WAL directory.
// close() must wait AFTER flushUpTo, not just before it.
func TestClose_WaitsForEagerJobTriggeredByItsOwnFlush(t *testing.T) {
	const segSize = int64(512)
	walDir := t.TempDir()

	w, err := NewWriter(Config{
		WALDir:      walDir,
		SegmentSize: segSize,
		Preallocate: true,
		// Large enough that every appended byte below stays buffered
		// in memory — nothing physically reaches a segment file until
		// Close's own flushUpTo drains it.
		WALBuffers: 64 * 1024,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Enough total bytes to span segment 0 into segment 1, so
	// flushUpTo's drain opens BOTH segment 0 and segment 1 for the
	// first time — each triggering its own fresh eager job (for
	// segment 1 and segment 2 respectively) that has had zero chance
	// to run before Close() is called. A9: the PG frame confines each
	// record to one segment (no cross-segment contrecord; the reserve
	// path pads to the boundary instead), so use two records that each
	// fit in a segment but together overflow segment 0 — the second is
	// relocated to segment 1.
	big := make([]byte, 300)
	if _, _, err := w.Append(big); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.Append(big); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.Append([]byte("tail")); err != nil {
		t.Fatal(err)
	}

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// If close() waited correctly, segment 2 (eagerly triggered by
	// flushUpTo's own first-time open of segment 1) must already be
	// fully sized the instant Close() returns — no explicit wait here.
	info, err := os.Stat(filepath.Join(walDir, formatSegmentName(2)))
	if err != nil {
		t.Fatalf("expected segment 2 to exist immediately after Close (eager job triggered by close's own flush): %v", err)
	}
	if info.Size() != segSize {
		t.Fatalf("segment 2 size = %d, want %d (Close returned before its own eager job finished)", info.Size(), segSize)
	}
}
