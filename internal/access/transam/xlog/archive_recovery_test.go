package xlog

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

func TestHighestLocalSegment(t *testing.T) {
	dir := t.TempDir()

	// Empty directory.
	n, err := highestLocalSegment(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n != -1 {
		t.Errorf("empty dir: got %d, want -1", n)
	}

	// Create some segment files.
	createSegment := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), make([]byte, 16), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	createSegment("000000010000000000000000")
	createSegment("000000010000000000000003")
	createSegment("000000010000000000000001")

	n, err = highestLocalSegment(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("after creates: got %d, want 3", n)
	}

	// Non-WAL files should be ignored.
	createSegment("not_a_wal_file")
	createSegment("00000001000000000000000") // one char short

	n, err = highestLocalSegment(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("after non-WAL files: got %d, want 3", n)
	}
}

// RunArchiveRecovery must fetch the segments that are missing locally via
// restore_command and stop — without error — as soon as the archive is
// exhausted (upstream xlogrecovery.c treats a failing restore_command as the
// normal end-of-archive signal, not a fault). This is the loop cmd/goopg's
// recovery.signal startup path calls, so a regression here turns a routine
// archive recovery into a refusal to start.
func TestRunArchiveRecoveryFetchesThenStops(t *testing.T) {
	const segSize = int64(1 << 20)
	archive := t.TempDir()
	walDir := t.TempDir()

	// Park one real segment in the archive. Its single record is a
	// checkpoint, which ApplyRecord treats as a replay no-op — that keeps
	// the test on the fetch/termination path without depending on
	// replayable heap payloads.
	w, err := NewWriter(Config{WALDir: archive, SegmentSize: segSize})
	if err != nil {
		t.Fatal(err)
	}
	_, recEnd, err := w.Append(make([]byte, 88))
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	segName := formatSegmentName(0)
	if _, err := os.Stat(filepath.Join(archive, segName)); err != nil {
		t.Fatalf("archive fixture segment missing: %v", err)
	}

	restoreCmd := "cp " + filepath.Join(archive, "%f") + " %p"

	dataDir := walDir
	if err := os.MkdirAll(filepath.Join(dataDir, "pg_wal"), 0o755); err != nil {
		t.Fatal(err)
	}
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	res, err := RunArchiveRecovery(mgr, dataDir, restoreCmd, segSize)
	endOfLog, lastStart := res.EndOfLog, res.LastStart
	if err != nil {
		t.Fatalf("RunArchiveRecovery: %v", err)
	}
	// EndOfLog — the promotion's timeline switch point — is the end of the
	// last record replayed from the restored segment.
	if endOfLog != recEnd {
		t.Errorf("endOfLog = %X, want the archived record's end %X", endOfLog, recEnd)
	}
	if lastStart == 0 || lastStart >= endOfLog {
		t.Errorf("lastStart = %X, want the archived record's start (< %X)", lastStart, endOfLog)
	}
	// Segment 0 came from the archive; segment 1 is absent there, which is
	// how the loop learns the archive is exhausted.
	if _, err := os.Stat(filepath.Join(dataDir, "pg_wal", segName)); err != nil {
		t.Errorf("segment 0 not restored into pg_wal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "pg_wal", formatSegmentName(1))); !os.IsNotExist(err) {
		t.Errorf("segment 1 should not exist (archive exhausted), stat err = %v", err)
	}
}

func TestRestoreArchivedFileEmptyCommand(t *testing.T) {
	err := RestoreArchivedFile("", "/tmp", "000000010000000000000001")
	if err == nil {
		t.Error("expected error for empty restore_command")
	}
}

func TestRestoreArchivedFileCommandFails(t *testing.T) {
	dir := t.TempDir()
	// Command that exits non-zero.
	err := RestoreArchivedFile("false", dir, "000000010000000000000001")
	if err == nil {
		t.Error("expected error for failing command")
	}
}

func TestRestoreArchivedFileSucceeds(t *testing.T) {
	dir := t.TempDir()
	segName := "000000010000000000000001"
	// Use a shell command that creates the file.
	err := RestoreArchivedFile("touch %p", dir, segName)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Verify the file exists.
	if _, err := os.Stat(filepath.Join(dir, segName)); err != nil {
		t.Errorf("segment file not created: %v", err)
	}
}

// TestRunArchiveRecoveryFollowsTimelineHistory pins the timeline-following
// archive recovery of PG's recovery_target_timeline = 'latest': the newest
// timeline is found from the archive's history files (findNewestTimeLine),
// its history gives the expected timelines (readTimeLineHistory), and each
// segment is read from the newest timeline that has it and began at or
// before it (XLogFileReadAnyTLI). The archive holds timeline 1 up to a
// switch in segment 1 and timeline 2 from there. goopg used to fetch only
// timeline-1 names, so it stopped at the switch and silently dropped every
// record written on timeline 2.
func TestRunArchiveRecoveryFollowsTimelineHistory(t *testing.T) {
	const segSize = int64(1 << 20)
	archive := t.TempDir()
	w, err := NewWriter(Config{WALDir: archive, SegmentSize: segSize})
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 2000)
	for w.WrittenLSN() < uint64(segSize)+uint64(segSize)/2 {
		if _, _, err := w.Append(payload); err != nil {
			t.Fatal(err)
		}
	}
	switchLSN := w.WrittenLSN()
	_, prev := w.InsertPosition()
	if err := w.SwitchTimeline(2, switchLSN, prev); err != nil {
		t.Fatal(err)
	}
	var end uint64
	for w.WrittenLSN() < 3*uint64(segSize)+uint64(segSize)/2 {
		if _, e, err := w.Append(payload); err != nil {
			t.Fatal(err)
		} else {
			end = e
		}
	}
	if err := w.FlushUpTo(w.WrittenLSN()); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := WriteHistory(archive, 2, []TimelineHistoryEntry{{TLI: 1, SwitchLSN: switchLSN, Reason: "test"}}); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	walDir := filepath.Join(dataDir, "pg_wal")
	if err := os.MkdirAll(walDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	res, err := RunArchiveRecovery(mgr, dataDir, "cp "+filepath.Join(archive, "%f")+" %p", segSize)
	if err != nil {
		t.Fatalf("RunArchiveRecovery: %v", err)
	}
	if res.NewestTLI != 2 || res.EndTLI != 2 {
		t.Errorf("newest TLI %d, end TLI %d, want 2 and 2", res.NewestTLI, res.EndTLI)
	}
	if res.EndOfLog != end {
		t.Errorf("EndOfLog = %X, want timeline 2's last record end %X", res.EndOfLog, end)
	}
	if len(res.Timelines) != 2 || res.Timelines[1] != (TimelineSpan{TLI: 2, Begin: switchLSN}) {
		t.Errorf("expected timelines = %+v, want [{1 0} {2 %X}]", res.Timelines, switchLSN)
	}
	if _, err := os.Stat(filepath.Join(walDir, TimelineHistoryFileName(2))); err != nil {
		t.Errorf("timeline 2's history file not restored: %v", err)
	}
	// Segment 1 holds the switch: timeline 2's copy is the one to read.
	if _, err := os.Stat(filepath.Join(walDir, FormatSegmentNameTLI(1, 2))); err != nil {
		t.Errorf("timeline 2's switch segment not restored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(walDir, FormatSegmentNameTLI(1, 1))); !os.IsNotExist(err) {
		t.Errorf("timeline 1's copy of the switch segment was restored (err %v); timeline 2's should win", err)
	}
}
