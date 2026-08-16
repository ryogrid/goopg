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
	if _, _, err := w.Append(make([]byte, 88)); err != nil {
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
	if err := RunArchiveRecovery(mgr, dataDir, restoreCmd, segSize); err != nil {
		t.Fatalf("RunArchiveRecovery: %v", err)
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
