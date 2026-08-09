package wal

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDetectWritePos_PromotedTimelineSegments is the regression pin for
// M0130-S10 blocker #6: goopg refused to START on a base backup taken from a
// PROMOTED PostgreSQL.
//
// A promoted cluster runs on timeline 2, so `pg_basebackup` hands over a
// pg_wal full of `00000002…` segments (plus `00000002.history`). goopg's
// detectWritePos discovered those files correctly — parseSegmentName ignores
// the TLI field — but then recomposed the filename with FormatSegmentName,
// which hardcodes TLI=1, and died before the listener ever bound:
//
//	goopg start: goopg: wal: wal: read <dir>/pg_wal/000000010000000000000002:
//	no such file or directory
//
// The reader side has been TLI-tolerant since openSegmentFile's fallback scan
// (reader.go); this pins its writer-side twin, which is exactly the class of
// divergence Hard-won Rule #2 exists for.
//
// The assertion is content-derived (the writePos of real appended records), so
// it cannot be satisfied by merely not-erroring: a run that opened the wrong
// file, or an all-zero one, reports the segment base instead.
func TestDetectWritePos_PromotedTimelineSegments(t *testing.T) {
	const segSize = int64(1024)
	walDir := t.TempDir()

	// Build genuine, decodable WAL through the writer (which always names
	// segments on TLI 1), then rename every segment onto TLI 2 — the exact
	// on-disk shape of a base backup from a promoted primary.
	lastEnd := buildRealWALSegments(t, walDir, segSize)
	retimelineSegments(t, walDir, 2)

	writePos, _, err := detectWritePos(walDir, segSize, false)
	if err != nil {
		t.Fatalf("detectWritePos over timeline-2 segments: %v "+
			"(pre-fix: ENOENT on the TLI=1 name goopg composed for itself)", err)
	}
	if writePos != int64(lastEnd) {
		t.Fatalf("writePos = %d, want %d (the real end of the timeline-2 segment)", writePos, lastEnd)
	}
}

// TestDetectWritePos_PrefersHighestTimeline pins WHICH file is read when the
// same segment number exists on two timelines. A promoted cluster keeps the
// pre-switch copy of the segment it switched inside of alongside the new
// timeline's copy, and only the newest timeline's copy carries the post-promotion
// records. Upstream resolves this in XLogFileReadAnyTLI, which walks expectedTLEs
// newest-timeline-first (postgres/src/backend/access/transam/xlog.c); goopg picks
// the highest TLI present on disk.
//
// Non-vacuity: the TLI-1 copy is zeroed to the same byte length, so reading it
// yields the segment base while reading the TLI-2 copy yields the record end.
// Both files parse to the same segment number, so a TLI-blind implementation
// cannot tell them apart.
func TestDetectWritePos_PrefersHighestTimeline(t *testing.T) {
	const segSize = int64(1024)
	walDir := t.TempDir()

	lastEnd := buildRealWALSegments(t, walDir, segSize)

	// Copy segment 0 to its timeline-2 name, keeping the real records, then
	// blank the timeline-1 original in place (same length).
	realName := filepath.Join(walDir, FormatSegmentNameTLI(0, 1))
	real, err := os.ReadFile(realName)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(walDir, FormatSegmentNameTLI(0, 2)), real, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(realName, make([]byte, len(real)), 0o644); err != nil {
		t.Fatal(err)
	}

	writePos, _, err := detectWritePos(walDir, segSize, false)
	if err != nil {
		t.Fatalf("detectWritePos: %v", err)
	}
	if writePos != int64(lastEnd) {
		t.Fatalf("writePos = %d, want %d — the timeline-1 copy (zeroed, base %d) was read instead of the timeline-2 copy",
			writePos, lastEnd, 0)
	}
}

// buildRealWALSegments appends three records through a real writer and returns
// the end LSN of the last one. Mirrors TestDetectWritePos_IgnoresEagerPhantomFutureSegment's
// setup: the point is a segment holding genuinely decodable records followed by
// a real zero-fill tail, so scanLastSegmentEnd has actual work to do.
func buildRealWALSegments(t *testing.T, walDir string, segSize int64) uint64 {
	t.Helper()
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: segSize, Preallocate: true})
	if err != nil {
		t.Fatal(err)
	}
	var lastEnd uint64
	for _, p := range []string{"alpha", "beta", "gamma"} {
		_, end, aerr := w.Append([]byte(p))
		if aerr != nil {
			t.Fatal(aerr)
		}
		lastEnd = end
	}
	if err := w.FlushUpTo(lastEnd); err != nil {
		t.Fatal(err)
	}
	w.stateRef.eagerWG.Wait()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if lastEnd == 0 {
		t.Fatal("buildRealWALSegments produced no records — the assertions below would be vacuous")
	}
	return lastEnd
}

// retimelineSegments renames every 24-hex-character WAL segment in walDir onto
// the given timeline, leaving the log+seg suffix untouched.
func retimelineSegments(t *testing.T, walDir string, tli uint32) {
	t.Helper()
	entries, err := os.ReadDir(walDir)
	if err != nil {
		t.Fatal(err)
	}
	renamed := 0
	for _, e := range entries {
		if e.IsDir() || len(e.Name()) != 24 {
			continue
		}
		_, segNo, ok := ParseXLogFileName(e.Name(), 0)
		if !ok {
			continue
		}
		to := FormatSegmentNameTLI(segNo, tli)
		if to == e.Name() {
			continue
		}
		if err := os.Rename(filepath.Join(walDir, e.Name()), filepath.Join(walDir, to)); err != nil {
			t.Fatal(err)
		}
		renamed++
	}
	if renamed == 0 {
		t.Fatalf("retimelineSegments renamed nothing in %s — the test would assert the pre-fix path", walDir)
	}
}
