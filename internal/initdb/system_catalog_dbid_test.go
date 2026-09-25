package initdb

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestRegisterSystemCatalogsForDBRoutesToOwnNamespace is the initdb-side gate
// for M0143-0002e (docs/design/0100-0149/m0143-0002d-per-database-type-catalog.md
// steps 2-3): CREATE DATABASE time registration (RegisterSystemCatalogsForDB,
// called from internal/postmaster's tryHandleDatabaseDDL) and the startup
// reload loop (loadSystemCatalogsIfPresent's cat.ListDatabases() pass) both
// build on catalog.RegisterRealTable's new dbOid argument. This test
// exercises the real physical-file path both depend on:
// CreatePerDatabaseScaffolding copies pg_type/pg_attribute from template0's
// bootstrap image before registration runs, exactly like a live CREATE
// DATABASE does (createDatabasePhysicalDirectory then
// registerSystemCatalogsForNewDB, in that order).
func TestRegisterSystemCatalogsForDBRoutesToOwnNamespace(t *testing.T) {
	dir := t.TempDir()
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	cat := catalog.NewInMemory()
	otherOid, err := cat.CreateDatabase("typedb", catalog.BootstrapSuperuserOID)
	if err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}

	// Mirrors createDatabasePhysicalDirectory: scaffold the new database's
	// own base/<oid>/ from template0's bootstrap image (copies 1247/1249).
	if err := CreatePerDatabaseScaffolding(dir, otherOid); err != nil {
		t.Fatalf("CreatePerDatabaseScaffolding: %v", err)
	}

	if err := RegisterSystemCatalogsForDB(dir, cat, otherOid); err != nil {
		t.Fatalf("RegisterSystemCatalogsForDB: %v", err)
	}

	typeTbl, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_type"}, otherOid)
	if !ok {
		t.Fatalf("pg_type not registered under otherOid %d namespace", otherOid)
	}
	if typeTbl.DBOid != otherOid {
		t.Errorf("pg_type.DBOid = %d, want %d", typeTbl.DBOid, otherOid)
	}
	attrTbl, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_attribute"}, otherOid)
	if !ok {
		t.Fatalf("pg_attribute not registered under otherOid %d namespace", otherOid)
	}
	if attrTbl.DBOid != otherOid {
		t.Errorf("pg_attribute.DBOid = %d, want %d", attrTbl.DBOid, otherOid)
	}

	if rel := cat.RelFileNode(typeTbl); rel.DBOid != otherOid || rel.RelOid != catalog.TypeRelationId {
		t.Errorf("RelFileNode(pg_type) = %+v, want DBOid=%d RelOid=%d", rel, otherOid, catalog.TypeRelationId)
	}

	// Re-running (idempotent — mirrors a second reload pass at the next
	// startup) must not error or duplicate the registration.
	if err := RegisterSystemCatalogsForDB(dir, cat, otherOid); err != nil {
		t.Fatalf("second RegisterSystemCatalogsForDB call: %v", err)
	}

	// A dbOid with no physical scaffolding at all is a silent no-op, same as
	// an old cluster missing the M0030-0001 relfiles.
	if err := RegisterSystemCatalogsForDB(dir, cat, 999999); err != nil {
		t.Errorf("RegisterSystemCatalogsForDB with no scaffolding: want nil error, got %v", err)
	}
	if _, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_type"}, 999999); ok {
		t.Errorf("pg_type registered for a dbOid with no scaffolding")
	}
}
