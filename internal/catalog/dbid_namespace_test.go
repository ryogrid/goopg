package catalog

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestDBOidParameterRoutesToDistinctNamespace exercises the optional trailing
// `dbOid ...uint32` parameter added to InMemory's table/index entry points
// (M0122-0007 slice 4b-ii, docs/design/0122-0018-per-database-catalog-namespace.md).
// No caller passes a real per-connection dbOid yet (that's 4c/4d) — this test
// is the only thing today that exercises ns()'s lazy-create branch with a
// dbOid other than DefaultDBOid, proving the plumbing actually isolates two
// namespaces instead of silently aliasing them.
func TestDBOidParameterRoutesToDistinctNamespace(t *testing.T) {
	c := NewInMemory()
	const otherDBOid = 999

	name := parser.ObjectName{Schema: "public", Name: "widgets"}
	cols := []Column{{Name: "id", Type: Type{Name: "int4"}}}

	// CreateTable under otherDBOid must not collide with, or become visible
	// under, DefaultDBOid.
	tbl, err := c.CreateTable(name, cols, otherDBOid)
	if err != nil {
		t.Fatalf("CreateTable(dbOid=%d) failed: %v", otherDBOid, err)
	}
	if _, ok := c.LookupTable(name); ok {
		t.Fatalf("LookupTable(default dbOid) unexpectedly found a table created under dbOid=%d", otherDBOid)
	}
	got, ok := c.LookupTable(name, otherDBOid)
	if !ok || got != tbl {
		t.Fatalf("LookupTable(dbOid=%d) = (%v, %v), want the table created above", otherDBOid, got, ok)
	}

	// A same-named table under DefaultDBOid is a wholly separate object.
	defTbl, err := c.CreateTable(name, cols)
	if err != nil {
		t.Fatalf("CreateTable(default dbOid) failed: %v", err)
	}
	if defTbl == tbl {
		t.Fatal("CreateTable(default dbOid) returned the same *Table as the dbOid=999 create")
	}

	// AllTables/TablesInSchema only see their own namespace.
	if all := c.AllTables(otherDBOid); len(all) != 1 || all[0].OID != tbl.OID {
		t.Fatalf("AllTables(dbOid=%d) = %v, want exactly the one table created there", otherDBOid, all)
	}
	if inSchema := c.TablesInSchema("public", otherDBOid); len(inSchema) != 1 {
		t.Fatalf("TablesInSchema(dbOid=%d) = %v, want exactly one entry", otherDBOid, inSchema)
	}

	// LookupTableByOID / InheritanceChildren / PartitionChildren resolve OIDs
	// within the given namespace only.
	if _, ok := c.LookupTableByOID(tbl.OID); ok {
		t.Fatalf("LookupTableByOID(default dbOid) unexpectedly found OID %d, which only exists under dbOid=%d", tbl.OID, otherDBOid)
	}
	if got, ok := c.LookupTableByOID(tbl.OID, otherDBOid); !ok || got != tbl {
		t.Fatalf("LookupTableByOID(dbOid=%d) = (%v, %v), want the table created above", otherDBOid, got, ok)
	}

	// CreateIndex/LookupIndex/AllIndexes/DropIndex under otherDBOid.
	idxName := parser.ObjectName{Schema: "public", Name: "widgets_id_idx"}
	idx, err := c.CreateIndex(idxName, tbl, []string{"id"}, true, "btree", true, otherDBOid)
	if err != nil {
		t.Fatalf("CreateIndex(dbOid=%d) failed: %v", otherDBOid, err)
	}
	if _, ok := c.LookupIndex(idxName); ok {
		t.Fatalf("LookupIndex(default dbOid) unexpectedly found an index created under dbOid=%d", otherDBOid)
	}
	if got, ok := c.LookupIndex(idxName, otherDBOid); !ok || got != idx {
		t.Fatalf("LookupIndex(dbOid=%d) = (%v, %v), want the index created above", otherDBOid, got, ok)
	}
	if all := c.AllIndexes(otherDBOid); len(all) != 1 || all[0].OID != idx.OID {
		t.Fatalf("AllIndexes(dbOid=%d) = %v, want exactly the one index created there", otherDBOid, all)
	}
	if _, ok := c.LookupIndexByOID(idx.OID); ok {
		t.Fatal("LookupIndexByOID(default dbOid) unexpectedly found an index that only exists under dbOid=999")
	}
	if got, ok := c.LookupIndexByOID(idx.OID, otherDBOid); !ok || got != idx {
		t.Fatalf("LookupIndexByOID(dbOid=%d) = (%v, %v), want the index created above", otherDBOid, got, ok)
	}

	// RenameTable/RenameIndex stay within their own namespace.
	renamed := parser.ObjectName{Schema: "public", Name: "widgets2"}
	if err := c.RenameTable(name, renamed, otherDBOid); err != nil {
		t.Fatalf("RenameTable(dbOid=%d) failed: %v", otherDBOid, err)
	}
	if _, ok := c.LookupTable(renamed, otherDBOid); !ok {
		t.Fatal("renamed table not found under dbOid=999 after RenameTable")
	}
	if _, ok := c.LookupTable(name); !ok {
		t.Fatal("RenameTable(dbOid=999) unexpectedly renamed the default-namespace table of the same original name")
	}

	// DropIndex/DropTable only remove from the targeted namespace.
	if err := c.DropIndex(idxName, otherDBOid); err != nil {
		t.Fatalf("DropIndex(dbOid=%d) failed: %v", otherDBOid, err)
	}
	if err := c.DropTable(renamed, otherDBOid); err != nil {
		t.Fatalf("DropTable(dbOid=%d) failed: %v", otherDBOid, err)
	}
	if _, ok := c.LookupTable(name); !ok {
		t.Fatal("DropTable(dbOid=999) unexpectedly dropped the default-namespace table")
	}
}
