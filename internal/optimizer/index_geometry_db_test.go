package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// TestEstimateIndexGeometryReadsFileInCatalogDatabase pins M0146-0178 slice 2
// at the planner: estimateIndexGeometry prices the index's real file, found in
// the catalog's storage database, as get_relation_info sizes the index it
// opened (plancat.c:466). Without the catalog the lookup missed the file and
// the width-derived guess stood (TPC-DS item_pkey: 40 pages against 52 real).
func TestEstimateIndexGeometryReadsFileInCatalogDatabase(t *testing.T) {
	c := catalog.NewInMemory()
	c.SetDBOID(5)
	tbl, err := c.CreateTable(parser.ObjectName{Name: "geo_t"}, []catalog.Column{{Name: "a", Type: catalog.Type{Name: "int4"}}})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := c.CreateIndex(parser.ObjectName{Name: "geo_t_pkey"}, tbl, []string{"a"}, true, "btree", true)
	if err != nil {
		t.Fatal(err)
	}
	prev := catalog.RelNBlocksFunc
	t.Cleanup(func() { catalog.RelNBlocksFunc = prev })
	catalog.RelNBlocksFunc = func(rel storage.RelFileNode) (int64, bool) {
		if rel.DBOid == 5 && rel.RelOid == idx.OID {
			return 52, true
		}
		return 0, false
	}
	if pages, _, _ := estimateIndexGeometry(c, idx, tbl, 18000); pages != 52 {
		t.Errorf("with the catalog: %v index pages, want the real 52", pages)
	}
	if pages, _, _ := estimateIndexGeometry(nil, idx, tbl, 18000); pages == 52 {
		t.Errorf("without the catalog the default-database lookup found the file; the case no longer reproduces")
	}
}
