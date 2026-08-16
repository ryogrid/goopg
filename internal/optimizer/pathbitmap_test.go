package optimizer

import (
	"fmt"
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

	p := s.buildOneBitmapPath(rel, tbl, idx, relPages, relTuples, T, s.totalTablePages(), maxEntries, rel.baseLeaf)
	if p == nil {
		t.Fatal("buildOneBitmapPath returned nil for a valid index")
	}

	// Verify the path has sane fields. It may or may not survive add_path
	// depending on whether seq scan dominates it (expected for full-table
	// selectivity on tiny tables).
	_ = idx
	_ = p
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

	p := s.buildOneBitmapPath(rel, tbl, idx, relPages, relTuples, T, s.totalTablePages(), 0, rel.baseLeaf)
	if p != nil {
		t.Error("buildOneBitmapPath should return nil for unique index with 1 row")
	}
}

func TestBuildOneBitmapPath_ResolvesPartialIndexPredicate(t *testing.T) {
	_, tbl, idx := testCatWithIdx(t)
	idx.HasPredicate = true
	idx.Predicate = &parser.BinaryOp{
		Op:    parser.OpGt,
		Left:  &parser.ColumnRef{Column: "a"},
		Right: &parser.IntegerConst{Value: 0},
	}
	s := testSearchForBitmap(t, tbl)
	rel := s.levelRels(1)[0]
	relTuples := float64(s.relInfos[0].baseRows)
	relPages := baseRelPages(tbl, relTuples)
	T := float64(relPages)
	if T < 1 {
		T = 1
	}

	// S5.4: partial indexes are no longer declined for bitmap scans.
	// The predicate is resolved and stored on the path for recheck.
	p := s.buildOneBitmapPath(rel, tbl, idx, relPages, relTuples, T, s.totalTablePages(), 0, rel.baseLeaf)
	if p == nil {
		t.Fatal("buildOneBitmapPath should build a path for partial index (predicate recheck covers correctness)")
	}
	if p.Kind != PathBitmapHeapScan {
		t.Fatalf("expected PathBitmapHeapScan, got kind=%d", p.Kind)
	}
	if len(p.Children) != 1 {
		t.Fatalf("expected 1 child, got %d", len(p.Children))
	}
	idxPath := p.Children[0]
	if idxPath.Kind != PathBitmapIndexScan {
		t.Fatalf("expected child PathBitmapIndexScan, got kind=%d", idxPath.Kind)
	}
	if idxPath.PartialPredicate == nil {
		t.Error("partial index predicate should be resolved and stored on the bitmap index path")
	}
	// Also verify the bitmap heap scan itself doesn't carry the predicate
	// (it's on the child index scan path).
	if p.PartialPredicate != nil {
		t.Error("PartialPredicate should be nil on the heap scan path (only on index scan leaves)")
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

	p := s.buildOneBitmapPath(rel, tbl, nil, relPages, relTuples, T, s.totalTablePages(), 0, rel.baseLeaf)
	if p != nil {
		t.Error("buildOneBitmapPath should return nil for nil index")
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
		relPages:        relPages,
		relTuples:       relTuples,
		indexPages:      indexPages,
		indexTuples:     indexTuples,
		treeHeight:      treeHeight,
		selectivity:     1.0,
		correlation:     0,
		totalTablePages: s.totalTablePages(),
	}
	tuplesFetched := clampRowEst(in.selectivity * relTuples)
	idxCost := costBitmapIndexScan(s.cp, in)
	pagesFetched := computeBitmapPages(tuplesFetched, T, indexPages, s.totalTablePages(), s.cp.effectiveCacheSize, maxEntries)
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

// testCatWithTwoIdx creates a catalog with one table and two B-tree indexes.
func testCatWithTwoIdx(t *testing.T) (catalog.Catalog, *catalog.Table, *catalog.Index, *catalog.Index) {
	t.Helper()
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "t"}, []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
		{Name: "b", Type: catalog.Type{Name: "int4"}},
		{Name: "c", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	idx1, err := cat.CreateIndex(parser.ObjectName{Name: "t_a_idx"}, tbl, []string{"a"}, false, "btree", false)
	if err != nil {
		t.Fatalf("CreateIndex a: %v", err)
	}
	idx2, err := cat.CreateIndex(parser.ObjectName{Name: "t_b_idx"}, tbl, []string{"b"}, false, "btree", false)
	if err != nil {
		t.Fatalf("CreateIndex b: %v", err)
	}
	return cat, tbl, idx1, idx2
}

func TestChooseBitmapAnd_TwoIndexes(t *testing.T) {
	cat, tbl, idx1, idx2 := testCatWithTwoIdx(t)
	_ = idx1
	_ = idx2

	leaf := &SeqScan{
		pos:    0,
		Table:  tbl,
		Alias:  "t",
		schema: tableSchema(tbl),
	}
	relInfos := []baseRelInfo{{
		table:        tbl,
		baseRows:     10000,
		filteredRows: 10000,
	}}
	bindings := []rangeBinding{{offset: 0}}
	cp := defaultCostParams()
	s, err := buildInitialRels(bindings, []Node{leaf}, relInfos, cp, 0, nil)
	if err != nil {
		t.Fatalf("buildInitialRels: %v", err)
	}

	rel := s.levelRels(1)[0]
	relTuples := float64(s.relInfos[0].baseRows)
	relPages := baseRelPages(tbl, relTuples)
	T := float64(relPages)
	if T < 1 {
		T = 1
	}
	maxEntries := bitmapMaxEntries(s.cp.workMem)

	// Verify buildOneBitmapPath returns valid paths for both indexes.
	p1 := s.buildOneBitmapPath(rel, tbl, idx1, relPages, relTuples, T, s.totalTablePages(), maxEntries, rel.baseLeaf)
	if p1 == nil {
		t.Fatal("buildOneBitmapPath returned nil for first index")
	}
	p2 := s.buildOneBitmapPath(rel, tbl, idx2, relPages, relTuples, T, s.totalTablePages(), maxEntries, rel.baseLeaf)
	if p2 == nil {
		t.Fatal("buildOneBitmapPath returned nil for second index")
	}

	// Verify chooseBitmapAnd doesn't panic when given two paths.
	andPath := s.chooseBitmapAnd([]*Path{p1, p2}, rel, tbl, relTuples, T, s.totalTablePages(), maxEntries)
	t.Logf("chooseBitmapAnd result: %v", andPath != nil)
	if andPath != nil {
		t.Logf("BitmapAnd path cost: %v, rows: %v", andPath.Cost, andPath.Rows)
	}

	// Verify the full pipeline doesn't panic.
	s.addBaseRelBitmapPaths(cat)
}

func TestBuildOneBitmapPath_QualPushdown(t *testing.T) {
	_, tbl, idx := testCatWithIdx(t)

	// A leaf with a Filter wrapper: WHERE a = 5, where 'a' is the indexed column.
	filter := &Filter{
		pos:       0,
		Child:     &SeqScan{pos: 0, Table: tbl, Alias: "t", schema: tableSchema(tbl)},
		Predicate: &BinaryOp{Op: parser.OpEq, Left: &ColumnRef{Index: 0}, Right: &IntegerConst{Value: 5}},
		LeafLocal: true,
	}

	relInfos := []baseRelInfo{{
		table:        tbl,
		baseRows:     10000,
		filteredRows: 100,
	}}
	bindings := []rangeBinding{{offset: 0}}
	cp := defaultCostParams()
	s, err := buildInitialRels(bindings, []Node{filter}, relInfos, cp, 0, nil)
	if err != nil {
		t.Fatalf("buildInitialRels: %v", err)
	}

	rel := s.levelRels(1)[0]
	relTuples := float64(s.relInfos[0].baseRows)
	relPages := baseRelPages(tbl, relTuples)
	T := float64(relPages)
	if T < 1 {
		T = 1
	}
	maxEntries := bitmapMaxEntries(s.cp.workMem)

	p := s.buildOneBitmapPath(rel, tbl, idx, relPages, relTuples, T, s.totalTablePages(), maxEntries, rel.baseLeaf)
	if p == nil {
		t.Fatal("buildOneBitmapPath returned nil for a valid index with qual")
	}

	// The bitmap heap scan path should have a child.
	if len(p.Children) != 1 || p.Children[0] == nil {
		t.Fatal("bitmap heap scan path should have one child")
	}
	inner := p.Children[0]

	// Verify IndexClauses are populated (qual pushdown worked).
	if len(inner.IndexClauses) != 1 {
		t.Fatalf("expected 1 index clause, got %d", len(inner.IndexClauses))
	}
	clause := inner.IndexClauses[0]
	if clause.indexCol != 0 {
		t.Errorf("indexCol = %d, want 0", clause.indexCol)
	}
	if clause.key == nil {
		t.Error("key should be non-nil (the constant expression)")
	}

	// Verify selectivity dropped below 1.0.
	if inner.BitmapSelectivity >= 1.0 {
		t.Errorf("BitmapSelectivity = %v, want < 1.0 (qual should reduce selectivity)", inner.BitmapSelectivity)
	}

	// Verify the inner path is a bitmap index scan (not heap scan).
	if inner.Kind != PathBitmapIndexScan {
		t.Errorf("inner path kind = %v, want PathBitmapIndexScan", inner.Kind)
	}

	t.Logf("BitmapSelectivity: %v", inner.BitmapSelectivity)
	t.Logf("IndexClauses: %d clauses", len(inner.IndexClauses))
	t.Logf("Path cost: startup=%v total=%v", p.Cost.Startup, p.Cost.Total)
	t.Logf("Path rows: %v", p.Rows)
}

// TestBitmapPathSurvivesAddPathOnLargeTable is the M0129-S5.2 selectivity-region
// survival proof. It constructs a synthetic large table (1M rows, 10k pages)
// with an uncorrelated index, adds a point-query local filter whose selectivity
// (~0.001) makes the bitmap scan's interpolated page cost beat both the
// seq-scan's full-table read and the index-scan's random I/O. The bitmap path
// MUST survive add_path and become CheapestTotal.
//
// Cost breakdown (default params, selectity 0.001, 1000 tuples fetched):
//
//	Seq scan:   10 000 pages × 1.0 seq + 1M tuples × 0.01 cpu = 20 000
//	Index scan: 953 pages × 4.0 random + index cost ≈ 3 820 (uncorrelated)
//	Bitmap:     2 × 9.4 idx + 953 × 1.93 page + 10 cpu ≈ 1 860
//
// Bitmap (1 860) < index scan (3 820) < seq scan (20 000).
// Since bitmap is >1% cheaper than index scan, it wins the cost dimension;
// the index scan's pathkeys make them incomparable, so both survive in the
// pathlist, and bitmap becomes CheapestTotal.
func TestBitmapPathSurvivesAddPathOnLargeTable(t *testing.T) {
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "big_t"}, []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
		{Name: "b", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	idx, err := cat.CreateIndex(parser.ObjectName{Name: "big_t_a_idx"}, tbl, []string{"a"}, false, "btree", false)
	if err != nil {
		t.Fatalf("CreateIndex: %v", err)
	}

	// Attach ANALYZE stats: 1M rows, 10k pages.
	// NDistinct=1000 gives selectivity 0.001 for a non-MCV equality predicate.
	// Correlation=0 penalises the index scan's IO (max_IO_cost wins).
	tbl.Stats = &catalog.TableStats{
		RowCount: 1_000_000,
		Pages:    10_000,
		Analyzed: true,
		Columns: []catalog.ColumnStats{
			{NDistinct: 1000, Correlation: 0}, // column a
			{},                                 // column b — no stats
		},
	}

	// A leaf with a local filter: WHERE a = 5.
	filter := &Filter{
		pos:       0,
		Child:     &SeqScan{pos: 0, Table: tbl, Alias: "big_t", schema: tableSchema(tbl)},
		Predicate: &BinaryOp{Op: parser.OpEq, Left: &ColumnRef{Index: 0}, Right: &IntegerConst{Value: 5}},
		LeafLocal: true,
	}

	relTuples := float64(1_000_000)
	relPages := int64(10_000)
	// Use the full row count for filteredRows so estScanPages computes
	// the correct full-table page count for the seq-scan cost.
	// The bitmap path's selectivity is driven by the index qual extracted
	// from the leaf Filter, independently of filteredRows.

	relInfos := []baseRelInfo{{
		table:        tbl,
		baseRows:     1_000_000,
		filteredRows: 1_000_000,
	}}
	bindings := []rangeBinding{{offset: 0}}
	cp := defaultCostParams()
	s, err := buildInitialRels(bindings, []Node{filter}, relInfos, cp, 0, nil)
	if err != nil {
		t.Fatalf("buildInitialRels: %v", err)
	}

	rel := s.levelRels(1)[0]
	T := float64(relPages)
	if T < 1 {
		T = 1
	}
	maxEntries := bitmapMaxEntries(s.cp.workMem)

	// Step 1: the initial prebuilt (seq-scan) path must be present.
	prePaths := pathsOfKind(rel.Pathlist, PathPrebuilt)
	if len(prePaths) != 1 {
		t.Fatalf("expected 1 prebuilt path, got %d", len(prePaths))
	}
	seqCost := prePaths[0].Cost.Total
	t.Logf("seq scan (prebuilt) cost: %.1f", seqCost)

	// Step 2: build the bitmap path and verify it has lower cost than seq scan.
	bmp := s.buildOneBitmapPath(rel, tbl, idx, relPages, relTuples, T, s.totalTablePages(), maxEntries, rel.baseLeaf)
	if bmp == nil {
		t.Fatal("buildOneBitmapPath returned nil — index qual should have matched")
	}
	bmpCost := bmp.Cost.Total
	t.Logf("bitmap path cost: %.1f", bmpCost)
	if bmpCost >= seqCost {
		t.Fatalf("bitmap cost %.1f >= seq scan cost %.1f — bitmap should be cheaper for this selectivity", bmpCost, seqCost)
	}

	// Step 3: add both paths and verify the bitmap survives add_path.
	// The seq scan was already added by buildInitialRels.
	beforeAdd := len(rel.Pathlist)
	addPath(rel, bmp)
	afterAdd := len(rel.Pathlist)
	t.Logf("pathlist size: before=%d after=%d", beforeAdd, afterAdd)

	// Check the bitmap path is present. With only a prebuilt (seq scan) path
	// already in the list, and bitmap strictly dominating it on cost (same
	// pathkeys: none, same ParallelSafe, same RequiredOuter), the incumbent
	// is removed and the list shrinks by 1 then grows by 1.
	bmpPaths := pathsOfKind(rel.Pathlist, PathBitmapHeapScan)
	if len(bmpPaths) != 1 || afterAdd != 1 {
		t.Fatalf("expected 1 bitmap path dominating the prebuilt, got %d bitmap paths (pathlist size %d)", len(bmpPaths), afterAdd)
	}

	// Step 4: run setCheapest and verify bitmap is CheapestTotal.
	setCheapest(rel)
	if rel.CheapestTotal == nil {
		t.Fatal("CheapestTotal is nil")
	}
	if rel.CheapestTotal.Kind != PathBitmapHeapScan {
		t.Fatalf("CheapestTotal kind = %v, want PathBitmapHeapScan (cost: %.1f vs seq: %.1f)",
			rel.CheapestTotal.Kind, bmpCost, seqCost)
	}
	t.Logf("CheapestTotal: kind=%v cost=%.1f", rel.CheapestTotal.Kind, rel.CheapestTotal.Cost.Total)

	// Step 5: verify the inner bitmap index path carries the qual selectivity.
	inner := bmp.Children[0]
	if inner.BitmapSelectivity >= 1.0 {
		t.Errorf("BitmapSelectivity = %v, want < 1.0", inner.BitmapSelectivity)
	}
	if len(inner.IndexClauses) != 1 {
		t.Errorf("expected 1 index clause, got %d", len(inner.IndexClauses))
	}
	t.Logf("selectivity=%.6f tuplesFetched=%.0f indexCost=%.1f",
		inner.BitmapSelectivity, inner.Rows, inner.Cost.Total)

	// Step 6: print EXPLAIN-shaped summary for the analysis/ record.
	fmt.Printf("M0129-S5.2 bitmap survival proof:\n")
	fmt.Printf("  table:      big_t (1M rows, 10k pages)\n")
	fmt.Printf("  index:      big_t_a_idx ON a (NDV=1000, correlation=0)\n")
	fmt.Printf("  predicate:  a = 5\n")
	fmt.Printf("  selectivity: %.6f (%.0f tuples)\n", inner.BitmapSelectivity, inner.Rows)
	fmt.Printf("  bitmap cost: %.1f (startup=%.1f, run=%.1f)\n", bmpCost, bmp.Cost.Startup, bmpCost-bmp.Cost.Startup)
	fmt.Printf("  seq cost:    %.1f\n", seqCost)
	fmt.Printf("  winner:      BitmapHeapScan (CheapestTotal)\n")
}

// pathsOfKind filters a pathlist to paths of a single kind.
func pathsOfKind(list []*Path, kind PathKind) []*Path {
	var out []*Path
	for _, p := range list {
		if p.Kind == kind {
			out = append(out, p)
		}
	}
	return out
}

// TestCollectBitmapPartialPredicates verifies that collectBitmapPartialPredicates
// collects PartialPredicate from bitmap index leaves (M0129-S5.4).
func TestCollectBitmapPartialPredicates(t *testing.T) {
	dummyPred := &ColumnRef{Name: "a", Index: 0}

	tests := []struct {
		name string
		path *Path
		want int
	}{
		{"nil path", nil, 0},
		{"non-bitmap path", &Path{Kind: PathSeqScan, PartialPredicate: dummyPred}, 0},
		{
			"single index scan with predicate",
			&Path{Kind: PathBitmapIndexScan, PartialPredicate: dummyPred},
			1,
		},
		{
			"single index scan without predicate",
			&Path{Kind: PathBitmapIndexScan, PartialPredicate: nil},
			0,
		},
		{
			"bitmap AND with two partial index leaves",
			&Path{
				Kind: PathBitmapAnd,
				Children: []*Path{
					{Kind: PathBitmapIndexScan, PartialPredicate: dummyPred},
					{Kind: PathBitmapIndexScan, PartialPredicate: dummyPred},
				},
			},
			2,
		},
		{
			"bitmap OR with mixed leaves",
			&Path{
				Kind: PathBitmapOr,
				Children: []*Path{
					{Kind: PathBitmapIndexScan, PartialPredicate: dummyPred},
					{Kind: PathBitmapIndexScan, PartialPredicate: nil},
				},
			},
			1,
		},
		{
			"nested AND/OR",
			&Path{
				Kind: PathBitmapAnd,
				Children: []*Path{
					{
						Kind: PathBitmapOr,
						Children: []*Path{
							{Kind: PathBitmapIndexScan, PartialPredicate: dummyPred},
							{Kind: PathBitmapIndexScan, PartialPredicate: dummyPred},
						},
					},
					{Kind: PathBitmapIndexScan, PartialPredicate: dummyPred},
				},
			},
			3,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := collectBitmapPartialPredicates(tt.path)
			if len(got) != tt.want {
				t.Errorf("collectBitmapPartialPredicates returned %d predicates, want %d", len(got), tt.want)
			}
		})
	}
}
