package catalog

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestLookupTableByOIDFallsBackToSystemCatalogNamespace covers the OID-based
// twin of LookupTable's system-catalog fallback (lookupSystemCatalogTableLocked,
// M0122-0007 4e): system catalogs (pg_class, pg_attribute, ...) are registered
// exactly once, under DefaultDBOid, by registerSystemTables — CREATE DATABASE
// never seeds a fresh namespace with its own copies. Before M0119-0006bp,
// LookupTableByOID had no equivalent fallback, so resolving a system catalog's
// own oid (e.g. pg_class = 1259) while scoped to any other, genuinely distinct
// dbOid always failed with "not found" — this is what real pg_amcheck hit
// walking pg_catalog.pg_class inside a non-default CREATE DATABASE'd database.
func TestLookupTableByOIDFallsBackToSystemCatalogNamespace(t *testing.T) {
	c := NewInMemory()
	const otherDBOid = 999

	// pg_class (oid 1259) lives only in DefaultDBOid's namespace.
	got, ok := c.LookupTableByOID(1259, otherDBOid)
	if !ok {
		t.Fatalf("LookupTableByOID(1259, dbOid=%d) = not found, want pg_class via the system-catalog fallback", otherDBOid)
	}
	if got.Schema != "pg_catalog" || got.Name != "pg_class" {
		t.Fatalf("LookupTableByOID(1259, dbOid=%d) = %s.%s, want pg_catalog.pg_class", otherDBOid, got.Schema, got.Name)
	}

	// Same lookup pinned to DefaultDBOid explicitly still works (unchanged
	// pre-fix path).
	got2, ok := c.LookupTableByOID(1259, DefaultDBOid)
	if !ok || got2 != got {
		t.Fatalf("LookupTableByOID(1259, DefaultDBOid) = (%v, %v), want the same *Table as the otherDBOid lookup", got2, ok)
	}

	// The fallback must stay scoped to pg_catalog/information_schema: a real
	// *user* table created under DefaultDBOid must NOT leak into a distinct
	// dbOid's OID lookup (isolation is the whole point of the per-DB
	// namespace work).
	name := parser.ObjectName{Schema: "public", Name: "widgets"}
	cols := []Column{{Name: "id", Type: Type{Name: "int4"}}}
	userTbl, err := c.CreateTable(name, cols) // DefaultDBOid
	if err != nil {
		t.Fatalf("CreateTable(default dbOid) failed: %v", err)
	}
	if leaked, ok := c.LookupTableByOID(userTbl.OID, otherDBOid); ok {
		t.Fatalf("LookupTableByOID(%d, dbOid=%d) unexpectedly found DefaultDBOid's user table %v — isolation broken", userTbl.OID, otherDBOid, leaked)
	}
}
