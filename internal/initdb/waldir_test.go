package initdb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInitRejectsRelativeWALDir mirrors upstream initdb.c:2961 — a
// relative --waldir argument is rejected before any filesystem layout,
// so no data directory is left behind.
func TestInitRejectsRelativeWALDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	err := Init(Options{DataDir: dir, WALDir: "pgxlog"})
	if err == nil {
		t.Fatal("Init accepted a relative WAL directory; want error")
	}
	if !strings.Contains(err.Error(), "absolute path") {
		t.Errorf("error = %q, want it to mention the absolute-path requirement", err.Error())
	}
	if _, statErr := os.Stat(dir); statErr == nil {
		t.Errorf("data dir %q was created despite the rejected relative WAL directory", dir)
	}
}

// TestInitRejectsNonEmptyWALDir mirrors upstream initdb's pg_check_dir
// cases 2/3/4: an existing, non-empty WAL directory (here holding a
// lost+found marker, exactly as 001_initdb.pl sets up) is rejected.
func TestInitRejectsNonEmptyWALDir(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "data")
	walDir := filepath.Join(tmp, "pgxlog")
	if err := os.MkdirAll(filepath.Join(walDir, "lost+found"), 0o700); err != nil {
		t.Fatalf("setup walDir: %v", err)
	}
	err := Init(Options{DataDir: dir, WALDir: walDir})
	if err == nil {
		t.Fatal("Init accepted a non-empty WAL directory; want error")
	}
	if !strings.Contains(err.Error(), "not empty") {
		t.Errorf("error = %q, want it to mention the directory is not empty", err.Error())
	}
}

// TestInitRelocatesWALDir is an end-to-end check that an absolute,
// empty/non-existent WAL directory is relocated: <data>/pg_wal becomes a
// symlink to walDir, and the archive_status/summaries subdirectories are
// created inside walDir through that symlink (matching the on-disk shape
// upstream initdb produces for -X/--waldir).
func TestInitRelocatesWALDir(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "data")
	walDir := filepath.Join(tmp, "pgxlog") // non-existent: Init must create it

	if err := Init(Options{DataDir: dir, WALDir: walDir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// pg_wal must be a symlink pointing at walDir.
	link := filepath.Join(dir, "pg_wal")
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("lstat pg_wal: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("pg_wal is not a symlink (mode %v)", info.Mode())
	}
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("readlink pg_wal: %v", err)
	}
	if target != walDir {
		t.Errorf("pg_wal -> %q, want %q", target, walDir)
	}

	// The WAL subdirectories must physically live inside walDir.
	for _, sub := range []string{"archive_status", "summaries"} {
		p := filepath.Join(walDir, sub)
		if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
			t.Errorf("expected directory %q inside the external WAL dir (err=%v)", p, err)
		}
	}
}

// TestInitDefaultWALDirIsPlainSubdir confirms the default (no WALDir)
// path is unchanged: pg_wal is a real directory under the data dir, not
// a symlink.
func TestInitDefaultWALDirIsPlainSubdir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	info, err := os.Lstat(filepath.Join(dir, "pg_wal"))
	if err != nil {
		t.Fatalf("lstat pg_wal: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Errorf("pg_wal is a symlink by default; want a plain directory")
	}
	if !info.IsDir() {
		t.Errorf("pg_wal is not a directory (mode %v)", info.Mode())
	}
}
