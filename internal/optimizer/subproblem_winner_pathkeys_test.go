package optimizer

import "testing"

// TestSubproblemWinnerLeafCarriesItsOrdering pins M0146-0222 slice 3. A searched
// sub-problem enters the enclosing search as a prebuilt leaf. PG's sub-joinlist
// joinrel keeps every path's pathkeys, the cheapest one included, so an ordering
// the sub-problem's winner delivers is visible to the joins and the grouping
// step above it. goopg translated only the runner-ups' orderings
// (addSubproblemAltPaths). TPC-DS Q72's sub-problem is won by a Gather Merge on
// the group keys once it is searched with consider_startup, and the grouping
// then sorted a second time.
func TestSubproblemWinnerLeafCarriesItsOrdering(t *testing.T) {
	s, err := newSearchCtx(2, defaultCostParams(), nil)
	if err != nil {
		t.Fatal(err)
	}
	scan := &SeqScan{schema: Schema{{Name: "a"}, {Name: "b"}}}
	winner := &Sort{Child: scan, Keys: []SortKey{{Expr: &ColumnRef{Index: 1, Name: "b"}}}}
	s.relInfos = []baseRelInfo{
		{bindingIdx: -1, sourceIdx: -1, subproblemAlts: func() []Node { return nil }},
		{sourceIdx: 3},
	}
	leaf := newRelOptInfo(relsetOf(0), 10, 32)
	leaf.baseLeaf = winner
	leaf.baseOffset = 4
	other := newRelOptInfo(relsetOf(1), 10, 32)
	other.baseLeaf = &SeqScan{schema: Schema{{Name: "c"}}}
	for _, r := range []*RelOptInfo{leaf, other} {
		if err := s.addRel(r); err != nil {
			t.Fatal(err)
		}
		addPath(r, newPrebuiltPath(r, r.baseLeaf), "test")
		setCheapest(r)
	}

	s.addCTEScanPathkeys()

	p := leaf.CheapestTotal
	if len(p.Pathkeys) != 1 {
		t.Fatalf("the sub-problem winner's leaf path has %d pathkeys, want its Sort's one key", len(p.Pathkeys))
	}
	cr, ok := p.Pathkeys[0].Expr.(*ColumnRef)
	if !ok || cr.Index != 5 || cr.Name != "b" {
		t.Fatalf("pathkey %#v: want the sort column at enclosing coordinate 4+1 = 5", p.Pathkeys[0].Expr)
	}
	if len(other.CheapestTotal.Pathkeys) != 0 {
		t.Fatalf("a plain base-relation leaf gained pathkeys")
	}
}
