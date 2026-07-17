package initdb

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// B0.3 (doc 02a §4): CREATE DATABASE seeds the new database with template0's
// pristine bootstrap catalog image. These pins cover copyBootstrapCatalogImage
// via CreatePerDatabaseScaffolding — the shared funnel of the server CREATE
// DATABASE path and its WAL-replay recovery.
func TestCreatePerDatabaseScaffoldingCopiesTemplate0Image(t *testing.T) {
	dataDir := t.TempDir()
	tmpl0 := filepath.Join(dataDir, "base", "4")
	if err := os.MkdirAll(tmpl0, 0o700); err != nil {
		t.Fatal(err)
	}
	// A synthetic pristine image: two catalog relfiles + the relmap +
	// template0's own PG_VERSION (which must NOT be copied — the new DB
	// writes its own).
	for name, content := range map[string]string{
		"2615":            "pg_namespace-heap",
		"2684":            "nspname-index",
		"pg_filenode.map": "relmap",
		"PG_VERSION":      "18\n",
	} {
		if err := os.WriteFile(filepath.Join(tmpl0, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	const newDb = uint32(16400)
	if err := CreatePerDatabaseScaffolding(dataDir, newDb); err != nil {
		t.Fatal(err)
	}
	newDir := filepath.Join(dataDir, "base", "16400")
	for name, want := range map[string]string{
		"2615":            "pg_namespace-heap",
		"2684":            "nspname-index",
		"pg_filenode.map": "relmap",
	} {
		got, err := os.ReadFile(filepath.Join(newDir, name))
		if err != nil {
			t.Fatalf("%s not copied: %v", name, err)
		}
		if string(got) != want {
			t.Fatalf("%s content = %q, want %q", name, got, want)
		}
	}
	// PG_VERSION is the scaffolding's own (CatalogVersion), not template0's.
	ver, err := os.ReadFile(filepath.Join(newDir, "PG_VERSION"))
	if err != nil {
		t.Fatal(err)
	}
	if string(ver) != CatalogVersion+"\n" {
		t.Fatalf("PG_VERSION = %q, want %q", ver, CatalogVersion+"\n")
	}

	// Idempotency (WAL replay over a live database dir): a file mutated
	// after creation must NOT be clobbered by a second scaffolding call.
	if err := os.WriteFile(filepath.Join(newDir, "2615"), []byte("post-create rows"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CreatePerDatabaseScaffolding(dataDir, newDb); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(newDir, "2615"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "post-create rows" {
		t.Fatalf("replay clobbered a live catalog file: %q", got)
	}

	// A crash-torn copy (missing file) is healed by the replay call.
	if err := os.Remove(filepath.Join(newDir, "2684")); err != nil {
		t.Fatal(err)
	}
	if err := CreatePerDatabaseScaffolding(dataDir, newDb); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(newDir, "2684")); err != nil {
		t.Fatalf("replay did not heal the missing catalog file: %v", err)
	}
}

// TestCreatePerDatabaseScaffoldingSystemDBsAndMissingImage pins the two
// no-copy cases: the three system databases (initdb populates them itself)
// and a pre-B0.3 data dir without a template0 image.
func TestCreatePerDatabaseScaffoldingSystemDBsAndMissingImage(t *testing.T) {
	dataDir := t.TempDir()
	tmpl0 := filepath.Join(dataDir, "base", "4")
	if err := os.MkdirAll(tmpl0, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpl0, "2615"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// System DBs: no copy (postgres = 5).
	if err := CreatePerDatabaseScaffolding(dataDir, catalog.PostgresDBOid); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "base", "5", "2615")); !os.IsNotExist(err) {
		t.Fatalf("system DB must not receive the image copy (err=%v)", err)
	}

	// Missing image: silent no-op for a user DB.
	dataDir2 := t.TempDir()
	if err := CreatePerDatabaseScaffolding(dataDir2, 16401); err != nil {
		t.Fatalf("missing template0 image must be a no-op, got %v", err)
	}
}
