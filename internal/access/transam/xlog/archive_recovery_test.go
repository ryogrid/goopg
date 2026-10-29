package xlog

import (
	"context"
	"errors"
	"fmt"
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

// TestRunArchiveRecoveryWithReadsRestoreCommandPerFetch pins that
// restore_command (PGC_SIGHUP) is read before every archive fetch, as
// RestoreArchivedFile reads recoveryRestoreCommand at each call: a reload
// that changes it mid-recovery takes effect at the next fetch. The getter
// hands out a failing command until the history probe has run and the
// working one afterwards — a recovery that captured the first value would
// restore nothing.
func TestRunArchiveRecoveryWithReadsRestoreCommandPerFetch(t *testing.T) {
	const segSize = int64(1 << 20)
	archive := t.TempDir()
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
	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "pg_wal"), 0o755); err != nil {
		t.Fatal(err)
	}
	working := "cp " + filepath.Join(archive, "%f") + " %p"
	calls := 0
	getter := func() string {
		calls++
		if calls == 1 { // the 00000002.history probe
			return "false"
		}
		return working
	}
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	res, err := RunArchiveRecoveryWith(context.Background(), mgr, dataDir, getter, segSize, TargetTimeline{})
	if err != nil {
		t.Fatalf("RunArchiveRecoveryWith: %v", err)
	}
	if res.EndOfLog != recEnd {
		t.Errorf("EndOfLog = %X, want %X: the changed restore_command was not used", res.EndOfLog, recEnd)
	}
	// One read for the history probe, one per segment fetch (segment 0
	// restored, segment 1 the end of the archive).
	if calls < 3 {
		t.Errorf("restore_command read %d times, want once per fetch (>= 3)", calls)
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
	// readTimeLineHistory reads the target's history through RECOVERYHISTORY
	// and does not keep it: restoreTimeLineHistoryFiles keeps only
	// [checkpoint TLI, target). The entries are reported for the promotion.
	if _, err := os.Stat(filepath.Join(walDir, TimelineHistoryFileName(2))); !os.IsNotExist(err) {
		t.Errorf("the target timeline's history file was kept in pg_wal (err %v)", err)
	}
	if len(res.TargetHistory) != 1 || res.TargetHistory[0] != (TimelineHistoryEntry{TLI: 1, SwitchLSN: switchLSN, Reason: "test"}) {
		t.Errorf("TargetHistory = %+v, want timeline 2's file verbatim", res.TargetHistory)
	}
	if _, err := os.Stat(filepath.Join(walDir, RecoveryHistoryName)); !os.IsNotExist(err) {
		t.Errorf("RECOVERYHISTORY left in pg_wal (err %v)", err)
	}
	// Segment 1 holds the switch: timeline 2's copy is the one to read.
	if _, err := os.Stat(filepath.Join(walDir, FormatSegmentNameTLI(1, 2))); err != nil {
		t.Errorf("timeline 2's switch segment not restored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(walDir, FormatSegmentNameTLI(1, 1))); !os.IsNotExist(err) {
		t.Errorf("timeline 1's copy of the switch segment was restored (err %v); timeline 2's should win", err)
	}
}

// TestRunArchiveRecoveryTargetTimeline pins recovery_target_timeline's
// other goals (validateRecoveryParameters): 'current' and a numeric
// timeline follow that timeline's history instead of the newest one; a
// numeric timeline with no history file is FATAL "recovery target timeline
// N does not exist" (timeline 1 never has one). Whatever the target, the
// newest archived timeline is still found, so the promotion's new timeline
// cannot collide with it. goopg only ever followed 'latest'.
func TestRunArchiveRecoveryTargetTimeline(t *testing.T) {
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
	run := func(target TargetTimeline) (ArchiveRecoveryResult, error) {
		dataDir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dataDir, "pg_wal"), 0o755); err != nil {
			t.Fatal(err)
		}
		mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
		return RunArchiveRecoveryWith(context.Background(), mgr, dataDir, func() string { return "cp " + filepath.Join(archive, "%f") + " %p" }, segSize, target)
	}

	for _, c := range []struct {
		name   string
		target TargetTimeline
		tli    uint32
		end    uint64
	}{
		{"current", TargetTimeline{Goal: TargetTimelineCurrent}, 1, switchLSN},
		{"numeric 1", TargetTimeline{Goal: TargetTimelineNumeric, TLI: 1}, 1, switchLSN},
		{"numeric 2", TargetTimeline{Goal: TargetTimelineNumeric, TLI: 2}, 2, end},
		{"latest", TargetTimeline{}, 2, end},
	} {
		t.Run(c.name, func(t *testing.T) {
			res, err := run(c.target)
			if err != nil {
				t.Fatal(err)
			}
			if res.TargetTLI != c.tli || res.EndTLI != c.tli || res.EndOfLog != c.end {
				t.Errorf("target %d, end TLI %d, EndOfLog %X; want target %d ending at %X on it",
					res.TargetTLI, res.EndTLI, res.EndOfLog, c.tli, c.end)
			}
			if res.NewestTLI != 2 {
				t.Errorf("newest TLI %d, want 2 (the archive's newest, whatever the target)", res.NewestTLI)
			}
		})
	}
	for _, tli := range []uint32{5, 0} {
		_, err := run(TargetTimeline{Goal: TargetTimelineNumeric, TLI: tli})
		var fatal *RestoreFatalError
		if want := fmt.Sprintf("recovery target timeline %d does not exist", tli); !errors.As(err, &fatal) || fatal.Msg != want {
			t.Errorf("target %d: err = %v, want FATAL %q", tli, err, want)
		}
	}
}

// TestRunArchiveRecoveryKeepsIntermediateHistoryFiles pins
// restoreTimeLineHistoryFiles(checkpoint TLI, recoveryTargetTLI): with the
// archive at timeline 3 (1 -> 2 -> 3) and recovery starting on timeline 1,
// timeline 2's history file is copied into pg_wal and timeline 3's — the
// target's, read through RECOVERYHISTORY — is not.
func TestRunArchiveRecoveryKeepsIntermediateHistoryFiles(t *testing.T) {
	const segSize = int64(1 << 20)
	archive := t.TempDir()
	w, err := NewWriter(Config{WALDir: archive, SegmentSize: segSize})
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 2000)
	var switches []uint64
	for tli := uint32(2); tli <= 3; tli++ {
		for w.WrittenLSN() < uint64(tli-1)*uint64(segSize)+uint64(segSize)/2 {
			if _, _, err := w.Append(payload); err != nil {
				t.Fatal(err)
			}
		}
		sw := w.WrittenLSN()
		_, prev := w.InsertPosition()
		if err := w.SwitchTimeline(tli, sw, prev); err != nil {
			t.Fatal(err)
		}
		switches = append(switches, sw)
	}
	for w.WrittenLSN() < 2*uint64(segSize)+uint64(segSize)*3/4 {
		if _, _, err := w.Append(payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.FlushUpTo(w.WrittenLSN()); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	h2 := []TimelineHistoryEntry{{TLI: 1, SwitchLSN: switches[0], Reason: "first"}}
	h3 := append(append([]TimelineHistoryEntry(nil), h2...), TimelineHistoryEntry{TLI: 2, SwitchLSN: switches[1], Reason: "second"})
	if err := WriteHistory(archive, 2, h2); err != nil {
		t.Fatal(err)
	}
	if err := WriteHistory(archive, 3, h3); err != nil {
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
	if res.TargetTLI != 3 || res.EndTLI != 3 || len(res.TargetHistory) != 2 || res.TargetHistory[1].Reason != "second" {
		t.Fatalf("target %d, end %d, history %+v; want timeline 3 with its two-line history", res.TargetTLI, res.EndTLI, res.TargetHistory)
	}
	if got, err := ReadHistory(walDir, 2); err != nil || len(got) != 1 || got[0] != h2[0] {
		t.Errorf("timeline 2's history in pg_wal = %+v, %v; want it kept (restoreTimeLineHistoryFiles)", got, err)
	}
	if _, err := os.Stat(filepath.Join(walDir, TimelineHistoryFileName(3))); !os.IsNotExist(err) {
		t.Errorf("the target timeline 3's history file was kept in pg_wal (err %v)", err)
	}
}
