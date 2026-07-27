package initdb

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// checkModeRecursive walks every directory and regular file at or under dir
// (following a relocated pg_wal symlink, exactly as upstream initdb's
// check_mode_recursive does with find's follow_fast) and fails the test on the
// first entry whose permission bits differ from wantDir (directories) or
// wantFile (regular files). It is the Go analogue of
// PostgreSQL/Test/Utils.pm:check_mode_recursive.
func checkModeRecursive(t *testing.T, dir string, wantDir, wantFile fs.FileMode) {
	t.Helper()
	var walk func(root string, followTopSymlink bool)
	walk = func(root string, followTopSymlink bool) {
		info, err := os.Lstat(root)
		if err != nil {
			t.Fatalf("lstat %q: %v", root, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if !followTopSymlink {
				return
			}
			if info, err = os.Stat(root); err != nil {
				t.Fatalf("stat %q: %v", root, err)
			}
		}
		switch {
		case info.IsDir():
			if got := info.Mode().Perm(); got != wantDir {
				t.Errorf("dir %q mode = %04o, want %04o", root, got, wantDir)
			}
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatalf("readdir %q: %v", root, err)
			}
			for _, e := range entries {
				if e.Type()&os.ModeSymlink != 0 {
					continue
				}
				walk(filepath.Join(root, e.Name()), false)
			}
		case info.Mode().IsRegular():
			if got := info.Mode().Perm(); got != wantFile {
				t.Errorf("file %q mode = %04o, want %04o", root, got, wantFile)
			}
		}
	}
	walk(dir, false)
	// Descend through a relocated pg_wal symlink, like check_mode_recursive.
	pgWal := filepath.Join(dir, "pg_wal")
	if li, err := os.Lstat(pgWal); err == nil && li.Mode()&os.ModeSymlink != 0 {
		walk(pgWal, true)
	}
}

// TestInitAllowGroupAccess mirrors 001_initdb.pl's "successful creation with
// group access" + check_mode_recursive($datadir, 0750, 0640) case: with
// -g/--allow-group-access every directory in the cluster is 0750 and every
// file 0640, and log_file_mode = 0640 is seeded into postgresql.conf.
func TestInitAllowGroupAccess(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data_group")
	if err := Init(Options{DataDir: dir, AllowGroupAccess: true, NoSync: true}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	checkModeRecursive(t, dir, 0o750, 0o640)

	conf, err := os.ReadFile(filepath.Join(dir, "postgresql.conf"))
	if err != nil {
		t.Fatalf("read postgresql.conf: %v", err)
	}
	// replace_guc_value writes 0640 unquoted (quote=false), in place of the
	// commented template default.
	if !strings.Contains(string(conf), "log_file_mode = 0640") {
		t.Errorf("postgresql.conf missing `log_file_mode = 0640`; group access did not seed it")
	}
}

// TestInitDefaultIsOwnerOnly confirms the default (no -g) cluster is
// owner-only: directories 0700, files 0600 (PG_DIR_MODE_OWNER /
// PG_FILE_MODE_OWNER), and log_file_mode is left at the template default.
func TestInitDefaultIsOwnerOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	checkModeRecursive(t, dir, 0o700, 0o600)

	conf, err := os.ReadFile(filepath.Join(dir, "postgresql.conf"))
	if err != nil {
		t.Fatalf("read postgresql.conf: %v", err)
	}
	if strings.Contains(string(conf), "\nlog_file_mode = 0640") {
		t.Errorf("postgresql.conf seeded log_file_mode = 0640 without -g")
	}
}

// TestInitAllowGroupAccessWithWALDir verifies the -g + -X/--waldir
// combination: the relocated external WAL directory and its contents are
// relaxed to 0750/0640 too, so check_mode_recursive (which follows the pg_wal
// symlink) still passes across the whole cluster.
func TestInitAllowGroupAccessWithWALDir(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "data")
	walDir := filepath.Join(tmp, "pgxlog") // non-existent: Init creates it
	if err := Init(Options{DataDir: dir, WALDir: walDir, AllowGroupAccess: true, NoSync: true}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	// Whole tree, including the external WAL dir reached via the pg_wal
	// symlink, must satisfy 0750/0640.
	checkModeRecursive(t, dir, 0o750, 0o640)
	// And the external WAL dir directly (its own top-level mode).
	if info, err := os.Stat(walDir); err != nil {
		t.Fatalf("stat walDir: %v", err)
	} else if got := info.Mode().Perm(); got != 0o750 {
		t.Errorf("external WAL dir mode = %04o, want 0750", got)
	}
}
