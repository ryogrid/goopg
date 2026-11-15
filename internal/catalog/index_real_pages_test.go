package catalog

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// TestIndexRealPagesInUsesCatalogDatabase pins M0146-0178 slice 2. On a
// server catalog (SetDBOID(5)) an index created in the postgres database
// carries the namespace key DefaultDBOid (1) and its file sits under base/5.
// The package-level IndexRealPages looks under base/1 and finds nothing, so
// the planner priced a width guess (item_pkey: 40 pages against 52 real). IndexRealPagesIn resolves through the
// catalog, as get_relation_info sizes the index it opened; so does the
// pg_class view, whose postgres namespace is keyed DefaultDBOid.
func TestIndexRealPagesInUsesCatalogDatabase(t *testing.T) {
	c := NewInMemory()
	c.SetDBOID(5)
	tbl, err := c.CreateTable(parser.ObjectName{Name: "pages_t"}, []Column{{Name: "a", Type: Type{Name: "int4"}}})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := c.CreateIndex(parser.ObjectName{Name: "pages_t_pkey"}, tbl, []string{"a"}, true, "btree", true)
	if err != nil {
		t.Fatal(err)
	}
	if idx.DBOid != 0 && idx.DBOid != DefaultDBOid {
		t.Fatalf("index carries DBOid %d; the case under test is the postgres namespace key", idx.DBOid)
	}
	prev := RelNBlocksFunc
	t.Cleanup(func() { RelNBlocksFunc = prev })
	RelNBlocksFunc = func(rel storage.RelFileNode) (int64, bool) {
		if rel.DBOid == 5 && rel.RelOid == idx.OID {
			return 52, true
		}
		return 0, false
	}
	// Since M0149-0001a1 S3 the postgres namespace key IS its storage OID
	// (DefaultDBOid == PostgresDBOid == 5), so the package-level lookup finds
	// the file too; before S3 it looked under base/1 and missed.
	if got, ok := IndexRealPages(idx); !ok || got != 52 {
		t.Errorf("IndexRealPages = %d, %v; want 52 (namespace = storage since S3)", got, ok)
	}
	if got, ok := IndexRealPagesIn(c, idx); !ok || got != 52 {
		t.Errorf("IndexRealPagesIn = %d, %v; want 52 from database 5", got, ok)
	}
	if got, ok := IndexRealPagesIn(nil, idx); !ok || got != 52 {
		t.Errorf("IndexRealPagesIn(nil) = %d, %v; want the package-level 52", got, ok)
	}
	found := false
	for _, row := range c.PGClassRowsForDBOid(DefaultDBOid) {
		if len(row) > 1 && row[1] == "pages_t_pkey" {
			found = true
			if !rowHasCell(row, "52") {
				t.Errorf("pg_class row for pages_t_pkey %v carries no relpages 52", row)
			}
		}
	}
	if !found {
		t.Fatal("pg_class has no row for pages_t_pkey")
	}
}

func rowHasCell(row []string, want string) bool {
	for _, cell := range row {
		if cell == want {
			return true
		}
	}
	return false
}

// TestTableRealPagesInUsesCatalogDatabase is IndexRealPagesIn's sibling for
// heaps (M0146-0178 worker-count residual): a postgres-namespace table's file
// sits under the catalog's storage database, which TableRealPages missed.
func TestTableRealPagesInUsesCatalogDatabase(t *testing.T) {
	c := NewInMemory()
	c.SetDBOID(5)
	tbl, err := c.CreateTable(parser.ObjectName{Name: "heap_t"}, []Column{{Name: "a", Type: Type{Name: "int4"}}})
	if err != nil {
		t.Fatal(err)
	}
	prev := RelNBlocksFunc
	t.Cleanup(func() { RelNBlocksFunc = prev })
	RelNBlocksFunc = func(rel storage.RelFileNode) (int64, bool) {
		if rel.DBOid == 5 && rel.RelOid == tbl.OID {
			return 1284, true
		}
		return 0, false
	}
	// Namespace = storage since M0149-0001a1 S3 (see the index sibling).
	if got, ok := TableRealPages(tbl); !ok || got != 1284 {
		t.Errorf("TableRealPages = %d, %v; want 1284 (namespace = storage since S3)", got, ok)
	}
	if got, ok := TableRealPagesIn(c, tbl); !ok || got != 1284 {
		t.Errorf("TableRealPagesIn = %d, %v; want 1284 from database 5", got, ok)
	}
}
