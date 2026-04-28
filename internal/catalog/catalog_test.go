package catalog

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestCatalogCreateLookupDrop pins the round-trip: a created table is
// found by both LookupTable and LookupColumn (case-insensitive), and
// can be dropped.
func TestCatalogCreateLookupDrop(t *testing.T) {
	c := NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "pgbench_accounts"}, []Column{
		{Name: "aid", Type: Type{Name: "int4"}, NotNull: true},
		{Name: "abalance", Type: Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if tbl.OID < FirstUserOID {
		t.Errorf("OID=%d want >= %d", tbl.OID, FirstUserOID)
	}
	if tbl.Columns[0].Ordinal != 0 || tbl.Columns[1].Ordinal != 1 {
		t.Errorf("ordinals not assigned")
	}

	got, ok := c.LookupTable(parser.ObjectName{Name: "pgbench_accounts"})
	if !ok || got.OID != tbl.OID {
		t.Fatalf("LookupTable round-trip failed: ok=%v", ok)
	}
	col, ok := c.LookupColumn(got, "ABALANCE")
	if !ok || col.Ordinal != 1 {
		t.Fatalf("LookupColumn case-insensitive failed: ok=%v col=%+v", ok, col)
	}

	rfn := c.RelFileNode(got)
	if rfn.RelOid != tbl.OID || rfn.DBOid != DefaultDBOid {
		t.Errorf("RelFileNode=%+v", rfn)
	}

	if err := c.DropTable(parser.ObjectName{Name: "pgbench_accounts"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.LookupTable(parser.ObjectName{Name: "pgbench_accounts"}); ok {
		t.Errorf("table should be gone after DropTable")
	}
}

// TestCatalogDuplicateAndMissing locks down the error paths.
func TestCatalogDuplicateAndMissing(t *testing.T) {
	c := NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "t"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTable(parser.ObjectName{Name: "t"}, nil); err == nil {
		t.Error("duplicate CreateTable should fail")
	}
	if err := c.DropTable(parser.ObjectName{Name: "missing"}); err == nil {
		t.Error("DropTable of missing should fail")
	}
}
