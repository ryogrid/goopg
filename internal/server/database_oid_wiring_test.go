package server

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
)

// TestWireExtensionRowsStampsCurrentDatabaseOid pins M0122-0007
// physical-storage-isolation slice 4a: wireExtensionRows — the single site
// shared by both the simple- and extended-query dispatch paths
// (M0110-0003 gap #7c) — must also resolve and stamp
// executor.Context.CurrentDatabaseOid with the REAL physical oid for the
// connecting database, not just CurrentDatabase (the name).
func TestWireExtensionRowsStampsCurrentDatabaseOid(t *testing.T) {
	im := catalog.NewInMemory()
	im.SetDBOID(5) // mirrors detectCatalogDBOID's real-world PG18 "postgres" oid
	s := New(Config{Catalog: im})

	ectx := executor.NewContext()
	s.wireExtensionRows(ectx, "postgres")
	if ectx.CurrentDatabase != "postgres" {
		t.Errorf("CurrentDatabase = %q, want %q", ectx.CurrentDatabase, "postgres")
	}
	if ectx.CurrentDatabaseOid != 5 {
		t.Errorf("CurrentDatabaseOid = %d, want 5 (the real postgres oid)", ectx.CurrentDatabaseOid)
	}

	oid, err := im.CreateDatabase("otherdb", catalog.BootstrapSuperuserOID)
	if err != nil {
		t.Fatalf("CreateDatabase(otherdb): %v", err)
	}
	ectx2 := executor.NewContext()
	s.wireExtensionRows(ectx2, "otherdb")
	if ectx2.CurrentDatabaseOid != oid {
		t.Errorf("CurrentDatabaseOid = %d, want %d (otherdb's allocated oid)", ectx2.CurrentDatabaseOid, oid)
	}

	// An unresolvable database name leaves CurrentDatabaseOid at its zero
	// value rather than stamping a wrong/stale oid.
	ectx3 := executor.NewContext()
	s.wireExtensionRows(ectx3, "no-such-database")
	if ectx3.CurrentDatabaseOid != 0 {
		t.Errorf("CurrentDatabaseOid = %d, want 0 for an unresolvable database name", ectx3.CurrentDatabaseOid)
	}
}
