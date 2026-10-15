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

// TestUpperSplitWorkersSizeOnCatalogDatabase pins M0146-0178's worker-count
// residual. An upper split's worker count is compute_parallel_worker over the
// driving scan's size — the INDEX's pages for an index-only scan — and both
// the heap and the index must be read where the catalog stores them. Without
// the catalog the default-database lookups miss a postgres-namespace
// relation's files and the count falls back to a statistics guess (here
// zero workers for a table the files say is 100000 pages).
func TestUpperSplitWorkersSizeOnCatalogDatabase(t *testing.T) {
	c := catalog.NewInMemory()
	c.SetDBOID(5)
	tbl, err := c.CreateTable(parser.ObjectName{Name: "w_t"}, []catalog.Column{{Name: "a", Type: catalog.Type{Name: "int4"}, NotNull: true}})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := c.CreateIndex(parser.ObjectName{Name: "w_t_pkey"}, tbl, []string{"a"}, true, "btree", true)
	if err != nil {
		t.Fatal(err)
	}
	tbl.Stats = &catalog.TableStats{RowCount: 10, Analyzed: true}
	prev := catalog.RelNBlocksFunc
	t.Cleanup(func() { catalog.RelNBlocksFunc = prev })
	catalog.RelNBlocksFunc = func(rel storage.RelFileNode) (int64, bool) {
		if rel.DBOid != 5 {
			return 0, false
		}
		switch rel.RelOid {
		case tbl.OID:
			return 100_000, true
		case idx.OID:
			return 30_000, true
		}
		return 0, false
	}
	cp := defaultCostParams()
	cp.maxParallelWorkersPerGather = 8
	scan := &IndexOnlyScan{Table: tbl, Index: idx}
	withCat := upperSplitWorkersForScan(scan, cp, c)
	without := upperSplitWorkersForScan(scan, cp, nil)
	want := computeParallelWorkerForRel(cp, 30_000, 0)
	if withCat != want || withCat == 0 {
		t.Errorf("with the catalog: %d workers, want %d (index-only scan sized on its 30000-page index)", withCat, want)
	}
	if without == withCat {
		t.Errorf("without the catalog the count is also %d; the default-database miss no longer reproduces", without)
	}
}
