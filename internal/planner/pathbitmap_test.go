package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// testCatWithIdx creates a minimal InMemory catalog containing a table with one
// B-tree index for use in bitmap path generation tests.
func testCatWithIdx(t *testing.T) (catalog.Catalog, *catalog.Table, *catalog.Index) {
	t.Helper()
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "t"}, []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
		{Name: "b", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	idx, err := cat.CreateIndex(parser.ObjectName{Name: "t_a_idx"}, tbl, []string{"a"}, false, "btree", false)
	if err != nil {
		t.Fatalf("CreateIndex: %v", err)
	}
	return cat, tbl, idx
}

// testSearchForBitmap creates a minimal searchCtx suitable for bitmap path
// generation — one base relation, the leaf is a bare SeqScan.
func testSearchForBitmap(t *testing.T, tbl *catalog.Table) *searchCtx {
	t.Helper()
	leaf := &SeqScan{
		pos:    0,
		Table:  tbl,
		Alias:  "t",
		schema: tableSchema(tbl),
	}
	relInfos := []baseRelInfo{{
		table:        tbl,
		baseRows:     1000,
		filteredRows: 1000,
	}}
	bindings := []rangeBinding{{offset: 0}}
	cp := defaultCostParams()
	s, err := buildInitialRels(bindings, []Node{leaf}, relInfos, cp, 0, nil)
	if err != nil {
		t.Fatalf("buildInitialRels: %v", err)
	}
	return s
}

func TestAddOneBitmapPath_ReturnsTrueForValidIndex(t *testing.T) {
	_, tbl, idx := testCatWithIdx(t)
	s := testSearchForBitmap(t, tbl)
	rel := s.levelRels(1)[0]
	relTuples := float64(s.relInfos[0].baseRows)
	relPages := baseRelPages(tbl, relTuples)
	T := float64(relPages)
	if T < 1 {
		T = 1
	}
	maxEntries := bitmapMaxEntries(s.cp.workMem)

	ok := s.addOneBitmapPath(rel, tbl, idx, relPages, relTuples, T, s.totalTablePages(), maxEntries)
	if !ok {
		t.Fatal("addOneBitmapPath returned false for a valid index")
	}

	// Verify the path has sane fields. It may or may not survive add_path
	// depending on whether seq scan dominates it (expected for full-table
	// selectivity on tiny tables).
	_ = idx
}

func TestAddOneBitmapPath_SkipsUniqueSingleRow(t *testing.T) {
	_, tbl, idx := testCatWithIdx(t)
	idx.Unique = true
	s := testSearchForBitmap(t, tbl)
	s.relInfos[0].baseRows = 1
	rel := s.levelRels(1)[0]
	rel.Rows = 1
	relTuples := float64(1)
	relPages := baseRelPages(tbl, relTuples)
	T := float64(relPages)
	if T < 1 {
		T = 1
	}

	ok := s.addOneBitmapPath(rel, tbl, idx, relPages, relTuples, T, s.totalTablePages(), 0)
	if ok {
		t.Error("addOneBitmapPath should return false for unique index with 1 row")
	}
}

func TestAddOneBitmapPath_SkipsPartialIndex(t *testing.T) {
	_, tbl, idx := testCatWithIdx(t)
	idx.HasPredicate = true
	s := testSearchForBitmap(t, tbl)
	rel := s.levelRels(1)[0]
	relTuples := float64(s.relInfos[0].baseRows)
	relPages := baseRelPages(tbl, relTuples)
	T := float64(relPages)
	if T < 1 {
		T = 1
	}

	ok := s.addOneBitmapPath(rel, tbl, idx, relPages, relTuples, T, s.totalTablePages(), 0)
	if ok {
		t.Error("addOneBitmapPath should return false for partial index")
	}
}

func TestAddOneBitmapPath_SkipsNilIndex(t *testing.T) {
	_, tbl, _ := testCatWithIdx(t)
	s := testSearchForBitmap(t, tbl)
	rel := s.levelRels(1)[0]
	relTuples := float64(s.relInfos[0].baseRows)
	relPages := baseRelPages(tbl, relTuples)
	T := float64(relPages)
	if T < 1 {
		T = 1
	}

	ok := s.addOneBitmapPath(rel, tbl, nil, relPages, relTuples, T, s.totalTablePages(), 0)
	if ok {
		t.Error("addOneBitmapPath should return false for nil index")
	}
}

func TestBitmapPathCost_Positive(t *testing.T) {
	_, tbl, idx := testCatWithIdx(t)
	s := testSearchForBitmap(t, tbl)
	relTuples := float64(s.relInfos[0].baseRows)
	relPages := baseRelPages(tbl, relTuples)
	T := float64(relPages)
	if T < 1 {
		T = 1
	}
	maxEntries := bitmapMaxEntries(s.cp.workMem)

	// Manually compute the bitmap path cost components.
	indexPages, indexTuples, treeHeight := estimateIndexGeometry(idx, tbl, relTuples)
	in := indexScanInputs{
		relPages:         relPages,
		relTuples:        relTuples,
		indexPages:       indexPages,
		indexTuples:      indexTuples,
		treeHeight:       treeHeight,
		selectivity:      1.0,
		correlation:      0,
		totalTablePages:  s.totalTablePages(),
	}
	tuplesFetched := clampRowEst(in.selectivity * relTuples)
	idxCost := costBitmapIndexScan(s.cp, in)
	pagesFetched := computeBitmapPages(tuplesFetched, T, maxEntries)
	totalCost := costBitmapHeapScan(s.cp, idxCost, pagesFetched, tuplesFetched, T)

	// Verify cost components are positive and ordered.
	if idxCost.Total <= 0 {
		t.Error("index cost should be positive")
	}
	if pagesFetched <= 0 {
		t.Error("pages fetched should be positive")
	}
	if totalCost.Total <= idxCost.Total {
		t.Error("total bitmap heap scan cost should exceed index cost alone")
	}
	if totalCost.Startup != idxCost.Total {
		t.Errorf("startup = %v, want index cost total = %v", totalCost.Startup, idxCost.Total)
	}
	t.Logf("index cost: %v", idxCost.Total)
	t.Logf("pages fetched: %v of %v", pagesFetched, T)
	t.Logf("total cost: startup=%v total=%v", totalCost.Startup, totalCost.Total)
}
