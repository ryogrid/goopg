package optimizer

import "testing"

// TestLoopCountReadsSubproblemBaseRows pins M0146-0222 slice 2 against
// get_loop_count (indxpath.c). A searched sub-problem enters the enclosing
// search as one leaf, but PG keeps a sub-joinlist as a joinrel whose base
// relids stay visible. A probe parameterized by a column of one base relation
// inside it therefore loops once per row of that relation. TPC-DS Q72's
// date_dim d3 probe on catalog_sales.cs_ship_date_sk is the case: the
// sub-problem leaf returns 1 row, catalog_sales has 360397, and with loop count
// 1 the probe was priced 0.29..8.31 against PG's 0.29..0.33.
func TestLoopCountReadsSubproblemBaseRows(t *testing.T) {
	s, err := newSearchCtx(2, defaultCostParams(), nil)
	if err != nil {
		t.Fatal(err)
	}
	const catalogSales int16 = 5
	s.relInfos = []baseRelInfo{
		{bindingIdx: -1, sourceIdx: -1, subproblemBaseRows: map[int16]float64{catalogSales: 360397, 6: 18000}},
		{sourceIdx: 9},
	}
	leaf := newRelOptInfo(relsetOf(0), 1, 32)
	probed := newRelOptInfo(relsetOf(1), 73049, 32)
	for _, r := range []*RelOptInfo{leaf, probed} {
		if err := s.addRel(r); err != nil {
			t.Fatal(err)
		}
	}
	req := relsetOf(0)
	key := []Expr{&ColumnRef{Name: "cs_ship_date_sk", SourceTableIdx: catalogSales}}

	if got := s.loopCountForKeys(probed, req, key); got != 360397 {
		t.Errorf("parameter from catalog_sales inside the leaf: loop count %v, want its base rows 360397", got)
	}
	if got := s.loopCountForKeys(probed, req, nil); got != 1 {
		t.Errorf("no parameter columns: loop count %v, want the leaf's own rows 1", got)
	}
	outside := []Expr{&ColumnRef{Name: "x", SourceTableIdx: 42}}
	if got := s.loopCountForKeys(probed, req, outside); got != 1 {
		t.Errorf("a column from no base relation of the leaf: loop count %v, want the leaf's rows 1", got)
	}
	// Two supplying base relations: the smallest, as get_loop_count takes.
	both := []Expr{&ColumnRef{SourceTableIdx: catalogSales}, &ColumnRef{SourceTableIdx: 6}}
	if got := s.loopCountForKeys(probed, req, both); got != 18000 {
		t.Errorf("two supplying relations: loop count %v, want the smaller 18000", got)
	}
}
