package initdb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInitSyncOnlyRejectsMissingDir mirrors 001_initdb.pl's
// `command_fails([ 'initdb', '--sync-only', "$tempdir/nonexistent" ])`:
// sync-only against a directory that does not exist must fail without
// creating anything (upstream initdb.c:3444 pg_check_dir(pg_data) <= 0 →
// "could not access directory").
func TestInitSyncOnlyRejectsMissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nonexistent")
	err := Init(Options{DataDir: dir, SyncOnly: true})
	if err == nil {
		t.Fatal("Init --sync-only accepted a missing directory; want error")
	}
	if !strings.Contains(err.Error(), "could not access directory") {
		t.Errorf("error = %q, want it to mention the inaccessible directory", err.Error())
	}
	if _, statErr := os.Stat(dir); statErr == nil {
		t.Errorf("sync-only created %q; it must not mutate the filesystem", dir)
	}
}

// TestInitSyncOnlyExistingDir mirrors 001_initdb.pl's
// `command_ok([ 'initdb', '--sync-only', $datadir ])`: sync-only against an
// already-initialized cluster succeeds and leaves the contents intact.
func TestInitSyncOnlyExistingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatalf("initial Init: %v", err)
	}
	// pg_control is the canonical proof the cluster was laid out.
	control := filepath.Join(dir, "global", "pg_control")
	before, err := os.ReadFile(control)
	if err != nil {
		t.Fatalf("read pg_control: %v", err)
	}

	if err := Init(Options{DataDir: dir, SyncOnly: true}); err != nil {
		t.Fatalf("Init --sync-only on an existing cluster: %v", err)
	}

	after, err := os.ReadFile(control)
	if err != nil {
		t.Fatalf("read pg_control after sync-only: %v", err)
	}
	if string(before) != string(after) {
		t.Error("sync-only altered pg_control; it must only flush, not rewrite")
	}
}

// TestInitSyncOnlyRejectsFile confirms a path that exists but is a regular
// file (not a directory) is rejected, matching the "could not access
// directory" guard rather than attempting to fsync a non-cluster.
func TestInitSyncOnlyRejectsFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatalf("setup file: %v", err)
	}
	err := Init(Options{DataDir: f, SyncOnly: true})
	if err == nil {
		t.Fatal("Init --sync-only accepted a regular file; want error")
	}
	if !strings.Contains(err.Error(), "could not access directory") {
		t.Errorf("error = %q, want the inaccessible-directory message", err.Error())
	}
}

// TestInitNoSyncStillCreatesCluster confirms -N/--no-sync produces the same
// on-disk layout as a default init (it only skips the trailing fsync, not
// any file creation). A cheap structural check on key paths is enough; the
// durability difference is not observable from userspace.
func TestInitNoSyncStillCreatesCluster(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatalf("Init --no-sync: %v", err)
	}
	for _, p := range []string{
		filepath.Join(dir, "PG_VERSION"),
		filepath.Join(dir, "global", "pg_control"),
		filepath.Join(dir, "base"),
		filepath.Join(dir, "pg_wal"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %q to exist after --no-sync init: %v", p, err)
		}
	}
}

// TestInitDefaultSyncsCleanly is a smoke test that the default path (fsync
// enabled) completes without error on a freshly laid-out cluster — i.e. the
// recursive fsync walk tolerates every file and directory initdb writes,
// including the pg_wal subtree.
func TestInitDefaultSyncsCleanly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("default Init (with fsync): %v", err)
	}
}

// TestInitSyncOnlyFollowsExternalWALSymlink confirms the sync walk descends
// into a relocated pg_wal: with an external WAL directory, pg_wal is a
// symlink, and fsyncDataDir must flush its target rather than silently
// skipping it (mirrors sync_pgdata's separate xlog_is_symlink pass).
func TestInitSyncOnlyFollowsExternalWALSymlink(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "data")
	walDir := filepath.Join(tmp, "pgxlog")
	if err := Init(Options{DataDir: dir, WALDir: walDir, NoSync: true}); err != nil {
		t.Fatalf("Init with external WAL dir: %v", err)
	}
	// sync-only must succeed even though pg_wal is a symlink to walDir.
	if err := Init(Options{DataDir: dir, SyncOnly: true}); err != nil {
		t.Fatalf("Init --sync-only with relocated pg_wal: %v", err)
	}
}
