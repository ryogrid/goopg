package initdb

import (
	"os"
	"path/filepath"
	"testing"
)

// removeInitFiles deletes both pg_internal.init files from a data directory,
// simulating what WAL recovery does when it replays RecordKindXactCommitInval
// records (unlinks the files but does not regenerate them).
func removeInitFiles(t *testing.T, dataDir string) {
	t.Helper()
	for _, p := range []string{
		filepath.Join(dataDir, "global", "pg_internal.init"),
		filepath.Join(dataDir, "base", "1", "pg_internal.init"),
		filepath.Join(dataDir, "base", "5", "pg_internal.init"),
	} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			t.Fatalf("removeInitFiles: %v", err)
		}
	}
}

// assertInitFilesExist checks that both pg_internal.init files are present.
func assertInitFilesExist(t *testing.T, dataDir string) {
	t.Helper()
	for _, p := range []string{
		filepath.Join(dataDir, "global", "pg_internal.init"),
		filepath.Join(dataDir, "base", "1", "pg_internal.init"),
		filepath.Join(dataDir, "base", "5", "pg_internal.init"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected init file %s to exist after refresh, got: %v", p, err)
		}
	}
}

// TestOpenRegeneratesInitFilesAfterRecoveryUnlink verifies that Open()
// regenerates pg_internal.init files even when they were unlinked (as
// crash-recovery WAL replay does for RecordKindXactCommitInval records).
// Without the M0106-0011 follow-up (b) fix, Open would complete without
// regenerating the files and PG standbys would fail to attach.
func TestOpenRegeneratesInitFilesAfterRecoveryUnlink(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatal(err)
	}
	// First Open + Close to ensure a clean cluster with init files present.
	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 16})
	if err != nil {
		t.Fatalf("initial Open: %v", err)
	}
	if err := rt.Close(); err != nil {
		t.Fatalf("initial Close: %v", err)
	}

	// Simulate WAL recovery unlinking the init files.
	removeInitFiles(t, dir)
	for _, p := range []string{
		filepath.Join(dir, "global", "pg_internal.init"),
		filepath.Join(dir, "base", "1", "pg_internal.init"),
	} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be absent before re-Open", p)
		}
	}

	// Re-Open: the post-recovery refresh should regenerate the files.
	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 16})
	if err != nil {
		t.Fatalf("post-unlink Open: %v", err)
	}
	defer rt2.Close()

	assertInitFilesExist(t, dir)
}

// TestCheckpointRegeneratesInitFiles verifies that CheckpointNow (via the
// PostCheckpointFn hook wired in Open) regenerates pg_internal.init files
// when they are absent. This is the "belt-and-suspenders" path: even if Open's
// post-recovery refresh did not run (e.g., the cluster was not restarted after
// unlink), the next checkpoint restores the files. M0106-0011 follow-up (b).
func TestCheckpointRegeneratesInitFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatal(err)
	}
	rt, err := Open(OpenOptions{DataDir: dir, PoolSlots: 16})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rt.Close()

	// Confirm init files are present after Open.
	assertInitFilesExist(t, dir)

	// Simulate WAL recovery unlink mid-operation.
	removeInitFiles(t, dir)

	// A checkpoint must regenerate the init files via PostCheckpointFn.
	if err := rt.Checkpointer.CheckpointNow(); err != nil {
		t.Fatalf("CheckpointNow: %v", err)
	}

	assertInitFilesExist(t, dir)
}
