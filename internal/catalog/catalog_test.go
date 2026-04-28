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

func TestCatalogIndexLifecycle(t *testing.T) {
	c := NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "items"}, []Column{{Name: "id", Type: Type{Name: "int4"}}})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := c.CreateIndex(parser.ObjectName{Name: "items_id_idx"}, tbl, []string{"id"}, false, "btree", false)
	if err != nil {
		t.Fatal(err)
	}
	if idx.OID <= tbl.OID {
		t.Fatalf("index oid=%d should be greater than table oid=%d", idx.OID, tbl.OID)
	}
	if _, ok := c.LookupIndex(parser.ObjectName{Name: "items_id_idx"}); !ok {
		t.Fatal("LookupIndex failed")
	}
	idxs := c.IndexesOnTable(tbl)
	if len(idxs) != 1 || idxs[0].Name != "items_id_idx" {
		t.Fatalf("IndexesOnTable=%+v", idxs)
	}
	rfn := c.IndexRelFileNode(idx)
	if rfn.RelOid != idx.OID || rfn.DBOid != DefaultDBOid {
		t.Fatalf("IndexRelFileNode=%+v", rfn)
	}
	if err := c.DropIndex(parser.ObjectName{Name: "items_id_idx"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.LookupIndex(parser.ObjectName{Name: "items_id_idx"}); ok {
		t.Fatal("index should be gone after DropIndex")
	}
}

func TestCatalogDropTableAlsoDropsIndexes(t *testing.T) {
	c := NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "t"}, []Column{{Name: "id", Type: Type{Name: "int4"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIndex(parser.ObjectName{Name: "t_pkey"}, tbl, []string{"id"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}
	if !c.HasPrimaryKey(tbl) {
		t.Fatal("expected HasPrimaryKey to be true")
	}
	if err := c.DropTable(parser.ObjectName{Name: "t"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.LookupIndex(parser.ObjectName{Name: "t_pkey"}); ok {
		t.Fatal("index metadata should be removed when table is dropped")
	}
}

func TestCatalogAddColumn(t *testing.T) {
	c := NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "items"}, []Column{{Name: "id", Type: Type{Name: "int4"}}})
	if err != nil {
		t.Fatal(err)
	}
	col, err := c.AddColumn(tbl, Column{Name: "label", Type: Type{Name: "text"}})
	if err != nil {
		t.Fatal(err)
	}
	if col.Ordinal != 1 {
		t.Fatalf("new column ordinal=%d want=1", col.Ordinal)
	}
	if _, err := c.AddColumn(tbl, Column{Name: "LABEL", Type: Type{Name: "text"}}); err == nil {
		t.Fatal("duplicate AddColumn should fail")
	}
}
