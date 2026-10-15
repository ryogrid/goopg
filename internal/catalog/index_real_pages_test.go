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
	if _, ok := IndexRealPages(idx); ok {
		t.Fatal("IndexRealPages found the file under the default database; the case no longer reproduces")
	}
	if got, ok := IndexRealPagesIn(c, idx); !ok || got != 52 {
		t.Errorf("IndexRealPagesIn = %d, %v; want 52 from database 5", got, ok)
	}
	if got, ok := IndexRealPagesIn(nil, idx); ok {
		t.Errorf("IndexRealPagesIn(nil) = %d; want the package-level miss", got)
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
