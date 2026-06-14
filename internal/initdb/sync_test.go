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
// symlink, and syncDataDir must flush its target rather than silently
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

// TestResolveSyncMethod ports parse_sync_method's acceptance/rejection set
// (src/fe_utils/option_utils.c:90): "" and "fsync" → FSYNC, "syncfs" → SYNCFS
// where supported, everything else → "unrecognized sync method".
func TestResolveSyncMethod(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    dataDirSyncMethod
		wantErr string
	}{
		{in: "", want: syncMethodFsync},
		{in: "fsync", want: syncMethodFsync},
		{in: "syncfs", want: syncMethodSyncfs}, // adjusted below when unsupported
		{in: "bogus", wantErr: "unrecognized sync method: bogus"},
		{in: "FSYNC", wantErr: "unrecognized sync method: FSYNC"}, // case-sensitive, like strcmp
	} {
		m, err := resolveSyncMethod(tc.in)
		if tc.in == "syncfs" && !syncfsSupported {
			if err == nil || !strings.Contains(err.Error(), "does not support sync method") {
				t.Errorf("resolveSyncMethod(%q) on a syncfs-less build: err=%v, want \"does not support\"", tc.in, err)
			}
			continue
		}
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("resolveSyncMethod(%q) err=%v, want %q", tc.in, err, tc.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("resolveSyncMethod(%q) unexpected err: %v", tc.in, err)
			continue
		}
		if m != tc.want {
			t.Errorf("resolveSyncMethod(%q) = %v, want %v", tc.in, m, tc.want)
		}
	}
}

// TestInitRejectsBogusSyncMethod confirms an unrecognized --sync-method is
// rejected before any filesystem work, for both the full-init and sync-only
// paths (parse_sync_method runs during option parsing upstream).
func TestInitRejectsBogusSyncMethod(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	err := Init(Options{DataDir: dir, SyncMethod: "bogus"})
	if err == nil || !strings.Contains(err.Error(), "unrecognized sync method") {
		t.Fatalf("Init with bogus sync method: err=%v, want \"unrecognized sync method\"", err)
	}
	if _, statErr := os.Stat(dir); statErr == nil {
		t.Errorf("Init laid out %q despite a bad sync method; validation must precede layout", dir)
	}
}

// TestInitSyncOnlySyncfs mirrors 001_initdb.pl's
// `command_ok([ 'initdb', '--sync-only', $datadir, '--sync-method' => 'syncfs' ])`
// on builds where syncfs is available (Linux). One syncfs(2) flushes the
// whole filesystem; the cluster contents must be untouched.
func TestInitSyncOnlySyncfs(t *testing.T) {
	if !syncfsSupported {
		t.Skip("syncfs sync method is not supported on this build")
	}
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatalf("initial Init: %v", err)
	}
	if err := Init(Options{DataDir: dir, SyncOnly: true, SyncMethod: "syncfs"}); err != nil {
		t.Fatalf("Init --sync-only --sync-method=syncfs: %v", err)
	}
}

// TestInitSyncOnlyNoSyncDataFiles mirrors 001_initdb.pl's
// `command_ok([ 'initdb', '--sync-only', '--no-sync-data-files', $datadir ])`:
// the option is accepted and sync-only still succeeds.
func TestInitSyncOnlyNoSyncDataFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatalf("initial Init: %v", err)
	}
	if err := Init(Options{DataDir: dir, SyncOnly: true, NoSyncDataFiles: true}); err != nil {
		t.Fatalf("Init --sync-only --no-sync-data-files: %v", err)
	}
}

// TestWalkAndFsyncExcludesBase proves that --no-sync-data-files' exclude path
// actually skips the base/ subtree (porting walkdir's
// `if (exclude_dir && strcmp(exclude_dir, path) == 0) return;`). We make base/
// unreadable so a walk that descends into it fails; with base/ excluded the
// walk must succeed, demonstrating the subtree was never visited. Skipped as
// root, where mode 0000 does not deny access.
func TestWalkAndFsyncExcludesBase(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission-based exclusion check is meaningless as root")
	}
	dir := t.TempDir()
	base := filepath.Join(dir, "base")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatalf("mkdir base: %v", err)
	}
	if err := os.WriteFile(filepath.Join(base, "1"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write base/1: %v", err)
	}
	if err := os.Chmod(base, 0o000); err != nil {
		t.Fatalf("chmod base 000: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(base, 0o700) })

	// Not excluded: descending into the unreadable base/ must surface an error.
	if err := walkAndFsync(dir, false, ""); err == nil {
		t.Error("walkAndFsync without exclusion accepted an unreadable base/; want error")
	}
	// Excluded: base/ is skipped, so the walk succeeds.
	if err := walkAndFsync(dir, false, base); err != nil {
		t.Errorf("walkAndFsync excluding base/ should skip it, got %v", err)
	}
}
