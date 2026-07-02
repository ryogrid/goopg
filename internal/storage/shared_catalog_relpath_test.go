package storage

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRelPathSharedCatalog verifies that DBOid 0 — goopg's sentinel for a
// cluster-wide shared catalog (e.g. pg_database, OID 1262) — resolves to
// global/<reloid> rather than base/0/<reloid>, mirroring PostgreSQL's
// relpath() global tablespace path. M0117-0008 Part B.
func TestRelPathSharedCatalog(t *testing.T) {
	mgr := NewManager(ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()

	shared := RelFileNode{DBOid: 0, RelOid: 1262, Fork: MainFork}
	if got, want := mgr.RelPath(shared), "global/1262"; got != want {
		t.Fatalf("RelPath(shared) = %q, want %q", got, want)
	}

	perDB := RelFileNode{DBOid: 5, RelOid: 16407, Fork: MainFork}
	if got, want := mgr.RelPath(perDB), "base/5/16407"; got != want {
		t.Fatalf("RelPath(perDB) = %q, want %q", got, want)
	}
}

// TestManagerOpensSharedCatalogUnderGlobalDir verifies that actually reading
// a block through the Manager for a DBOid-0 RelFileNode opens
// <dataDir>/global/<reloid> on disk, not <dataDir>/base/0/<reloid>.
func TestManagerOpensSharedCatalogUnderGlobalDir(t *testing.T) {
	dataDir := t.TempDir()
	mgr := NewManager(ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	shared := RelFileNode{DBOid: 0, RelOid: 1262, Fork: MainFork}
	page := make(Page, BlockSize)
	if err := InitPage(page); err != nil {
		t.Fatalf("InitPage: %v", err)
	}
	if _, err := mgr.Extend(shared, page); err != nil {
		t.Fatalf("Extend: %v", err)
	}

	globalPath := filepath.Join(dataDir, "global", "1262")
	if _, err := os.Stat(globalPath); err != nil {
		t.Fatalf("expected %s to exist: %v", globalPath, err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "base", "0", "1262")); err == nil {
		t.Fatal("base/0/1262 should NOT exist — shared catalogs must not use a per-DBOid-0 dir")
	}
}
