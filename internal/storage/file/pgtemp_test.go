package file

import (
	"os"
	"path/filepath"
	"testing"
)

// The directory layout is a compatibility contract, not an implementation
// detail: a PG binary attached to a goopg datadir (and every operator's
// muscle memory) expects spill files under base/pgsql_tmp.
func TestDirMatchesPGLayout(t *testing.T) {
	got := Dir("/var/lib/goopg/data")
	want := filepath.Join("/var/lib/goopg/data", "base", "pgsql_tmp")
	if got != want {
		t.Fatalf("Dir = %q, want %q", got, want)
	}
}

func TestFilePatternCarriesPrefix(t *testing.T) {
	got := FilePattern(4242)
	if want := "pgsql_tmp4242.*"; got != want {
		t.Fatalf("FilePattern = %q, want %q", got, want)
	}
}

// RemoveStrayFiles is the crash sweep. It must reclaim spill files a killed
// backend left behind, and must NOT touch anything else that happens to share
// the directory — PG's RemovePgTempFilesInDir(unlink_all=false) filters on the
// prefix for exactly that reason.
func TestRemoveStrayFilesRemovesOnlyPrefixed(t *testing.T) {
	dataDir := t.TempDir()
	dir, err := EnsureDir(dataDir)
	if err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	strays := []string{"pgsql_tmp111.abc", "pgsql_tmp222.def"}
	keep := []string{"do-not-touch", "README"}
	for _, name := range append(append([]string{}, strays...), keep...) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	// A prefixed SUBDIRECTORY (PG's shared file sets) goes whole.
	subdir := filepath.Join(dir, "pgsql_tmp333.fileset")
	if err := os.MkdirAll(subdir, 0o700); err != nil {
		t.Fatalf("mkdir fileset: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "member"), []byte("y"), 0o600); err != nil {
		t.Fatalf("seed fileset member: %v", err)
	}

	n, err := RemoveStrayFiles(dataDir)
	if err != nil {
		t.Fatalf("RemoveStrayFiles: %v", err)
	}
	if n != 3 {
		t.Fatalf("removed %d entries, want 3", n)
	}
	for _, name := range strays {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("stray %s survived the sweep", name)
		}
	}
	if _, err := os.Stat(subdir); !os.IsNotExist(err) {
		t.Errorf("stray fileset directory survived the sweep")
	}
	for _, name := range keep {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("non-temp file %s was removed: %v", name, err)
		}
	}
}

// A cluster that never spilled has no pgsql_tmp directory at all. Startup must
// not treat that as an error (PG's missing_ok=true).
func TestRemoveStrayFilesMissingDirIsNotAnError(t *testing.T) {
	n, err := RemoveStrayFiles(t.TempDir())
	if err != nil || n != 0 {
		t.Fatalf("RemoveStrayFiles on a fresh datadir = (%d, %v), want (0, nil)", n, err)
	}
}
