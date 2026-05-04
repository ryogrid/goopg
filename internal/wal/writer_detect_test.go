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
