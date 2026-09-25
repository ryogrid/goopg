package optimizer

// M0145-0006 slice 3 — `electOrderedGrouping` admits a positional-identity
// `*Project` chain over `agg.node`.
//
// The pointer-equality gate declined the measured RENAME case (TPC-DS Q21
// reaches the ordered seam as `Project{Aggregate}` relabelling two aggregate
// outputs), so the GROUP_AGG candidate set was never elected over for any
// statement that renames its aggregate outputs. `inputNodePathkeys`' walk had
// the same blind spot until M0144-0011a-3; this is its election-side twin, and
// both routes share `projectIsPositionalIdentity` so they cannot disagree
// about which projections are see-through.
//
// What the pins protect is the SPLICE. The elected spec is copied back onto
// `agg.node` in place, so the chain already carries it — but the built winner
// was assembled over the BARE aggregate, and returning that would drop the
// projection and publish the aggregate's labels instead of the statement's.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// renameProjectOver is the measured shape: a pure relabel of every column, at
// the same positions, over the aggregate.
func renameProjectOver(agg *Aggregate) *Project {
	out := agg.Output()
	targets := make([]Expr, len(out))
	renamed := make(Schema, len(out))
	for i, c := range out {
		targets[i] = &ColumnRef{Index: i, Name: c.Name, SourceTableIdx: c.SourceTableIdx}
		renamed[i] = SchemaColumn{Name: "alias" + string(rune('a'+i)), Type: c.Type, SourceTableIdx: c.SourceTableIdx}
	}
	return &Project{pos: 0, Child: agg, Targets: targets, schema: renamed}
}

// q4ShapedGroupedRel is the two-candidate election fixture
// TestElectOrderedGroupingElectsNoSortAtQ4Numbers uses: a genuine
// startup/total trade-off, so both candidates survive to the election.
func q4ShapedGroupedRel(t *testing.T) (*upperRels, *aggregateSurface, *ColumnRef) {
	t.Helper()
	aggNode, groupCol, outSchema := r47slice2GroupFixture()
	u := newUpperRels()
	grouped := fetchUpperRel(u, UpperGroupAgg, 0, 0)
	grouped.ConsiderStartup = true
	mkSpec := func() *Aggregate {
		return &Aggregate{Child: aggNode.Child, GroupExprs: []Expr{groupCol}, schema: outSchema}
	}
	sorted := r47slice2SortedCand(mkSpec(), []PathKey{{Expr: groupCol, SortAsc: true}}, Cost{Startup: 69094, Total: 70122})
	sorted.Rel = grouped
	addPath(grouped, sorted, "test")
	addPath(grouped, &Path{Kind: PathAgg, AggStrategy: AggStrategyHashed, Agg: mkSpec(),
		Rows: 5, Cost: Cost{Startup: 50000, Total: 71000}, Rel: grouped,
		Children: []*Path{newPrebuiltPath(grouped, aggNode.Child)}}, "test")
	setCheapest(grouped)
	return u, &aggregateSurface{node: aggNode}, groupCol
}

// TestElectOrderedGroupingElectsThroughARename pins the admission plus the
// no-sort splice: the election runs, the winning spec lands on `agg.node`, and
// the node handed back to the caller is still the PROJECT — not the bare
// aggregate the winner was built over.
func TestElectOrderedGroupingElectsThroughARename(t *testing.T) {
	u, agg, _ := q4ShapedGroupedRel(t)
	proj := renameProjectOver(agg.node)
	keys := []SortKey{{Expr: &ColumnRef{Index: 0, Name: "aliasa", Type: catalog.Type{Name: "bpchar"}}}}

	got, ok := electOrderedGrouping(u, agg, proj, keys, 0, DefaultPlannerSettings().costParams(), 0, -1, nil)
	if !ok || got == nil {
		t.Fatal("a rename over the aggregate declined; want the election")
	}
	if got != Node(proj) {
		t.Fatalf("winner is %T (%p); want the rename Project itself (%p)", got, got, proj)
	}
	if agg.node.Strategy != AggStrategySorted {
		t.Fatalf("copy-back strategy = %v; want sorted", agg.node.Strategy)
	}
	if proj.Child != Node(agg.node) {
		t.Fatal("the splice must leave the chain pointing at the updated aggregate")
	}
}

// TestElectOrderedGroupingReparentsTheSortOverTheRename is the other splice
// arm: when the winner needs a Sort, that Sort must sit over the RENAME, not
// over the aggregate — otherwise the projection is dropped from the plan.
func TestElectOrderedGroupingReparentsTheSortOverTheRename(t *testing.T) {
	u, agg, _ := q4ShapedGroupedRel(t)
	proj := renameProjectOver(agg.node)
	// ORDER BY the second output column: no candidate's emission order
	// delivers it, so the election takes its `create_sort_path` arm.
	keys := []SortKey{{Expr: &ColumnRef{Index: 1, Name: "aliasb", Type: catalog.Type{Name: "int8"}}}}

	got, ok := electOrderedGrouping(u, agg, proj, keys, 7, DefaultPlannerSettings().costParams(), 0, -1, nil)
	if !ok || got == nil {
		t.Fatal("a rename over the aggregate declined; want the Sort-over election")
	}
	srt, isSort := got.(*Sort)
	if !isSort {
		t.Fatalf("winner is %T; want a *Sort over the rename", got)
	}
	if srt.Child != Node(proj) {
		t.Fatalf("Sort child is %T; want the rename Project — re-parenting it over the bare aggregate drops the projection", srt.Child)
	}
}

// TestElectOrderedGroupingStillDeclinesAWiderWrapper keeps the relaxation
// narrow: only positional-identity projections are see-through. A HAVING
// filter, a non-identity projection and an unrelated aggregate all still
// decline, and a decline leaves the ORDERED rel pristine for the normal
// `createOrderedPaths` call.
func TestElectOrderedGroupingStillDeclinesAWiderWrapper(t *testing.T) {
	keys := []SortKey{{Expr: &ColumnRef{Index: 0, Name: "aliasa", Type: catalog.Type{Name: "bpchar"}}}}

	cases := map[string]func(*Aggregate) Node{
		"non-identity projection": func(a *Aggregate) Node {
			p := renameProjectOver(a)
			// Re-order the targets: positions ARE re-assigned, so a
			// positional claim from below would address the wrong column.
			p.Targets[0], p.Targets[1] = p.Targets[1], p.Targets[0]
			return p
		},
		"projection over a different aggregate": func(a *Aggregate) Node {
			other := *a
			return renameProjectOver(&other)
		},
	}
	for name, wrap := range cases {
		t.Run(name, func(t *testing.T) {
			u, agg, _ := q4ShapedGroupedRel(t)
			ordered := fetchUpperRel(u, UpperOrdered, 0, 0)
			before := len(ordered.Pathlist)

			got, ok := electOrderedGrouping(u, agg, wrap(agg.node), keys, 0, DefaultPlannerSettings().costParams(), 0, -1, nil)
			if ok || got != nil {
				t.Fatalf("elected through a %s (ok=%v); want the decline", name, ok)
			}
			if after := fetchUpperRel(u, UpperOrdered, 0, 0); len(after.Pathlist) != before {
				t.Fatalf("the decline mutated the ORDERED rel: %d -> %d paths", before, len(after.Pathlist))
			}
		})
	}
}

// --- slice 5: the HAVING filter, admitted WITH pricing ---------------------

// TestElectOrderedGroupingElectsThroughAHavingFilter pins the admission and
// the splice. The HAVING filter publishes its child's schema and removes rows
// without reordering them, so a positional ordering claim from below survives
// it — but the rows it removes have to be priced, which is what kept this case
// out until the candidates carried the qual.
func TestElectOrderedGroupingElectsThroughAHavingFilter(t *testing.T) {
	u, agg, _ := q4ShapedGroupedRel(t)
	having := &Filter{pos: 0, Child: agg.node, Predicate: &BinaryOp{
		Op:    parser.OpGt,
		Left:  &ColumnRef{Index: 1, Name: "count", Type: catalog.Type{Name: "int8"}},
		Right: &IntegerConst{Value: 1},
	}}
	keys := []SortKey{{Expr: &ColumnRef{Index: 0, Name: "o_orderpriority", Type: catalog.Type{Name: "bpchar"}}}}

	got, ok := electOrderedGrouping(u, agg, having, keys, 0, DefaultPlannerSettings().costParams(), 0, -1, nil)
	if !ok || got == nil {
		t.Fatal("a HAVING filter over the aggregate declined; want the election")
	}
	if got != Node(having) {
		t.Fatalf("winner is %T; want the HAVING Filter itself — dropping it would return rows the statement filters out", got)
	}
	if having.Child != Node(agg.node) {
		t.Fatal("the splice must leave the filter pointing at the updated aggregate")
	}
}

// TestApplyHavingQualsToOfferIsCostAggsQualBlock pins the arithmetic itself:
// the quals cost what they cost to evaluate over the emitted groups, and they
// reduce the row estimate by their selectivity. Without both halves the
// candidates would be priced over a row count the plan never produces.
func TestApplyHavingQualsToOfferIsCostAggsQualBlock(t *testing.T) {
	aggNode, _, _ := r47slice2GroupFixture()
	cp := DefaultPlannerSettings().costParams()
	qual := &BinaryOp{
		Op:    parser.OpGt,
		Left:  &ColumnRef{Index: 1, Name: "count", Type: catalog.Type{Name: "int8"}},
		Right: &IntegerConst{Value: 1},
	}

	base := Path{Rows: 100, Cost: Cost{Startup: 10, Total: 50}}
	offer := base
	applyHavingQualsToOffer(&offer, []Expr{qual}, aggNode, cp)
	if offer.Cost.Total <= base.Cost.Total {
		t.Fatalf("total cost %v did not rise; the qual evaluation is not free", offer.Cost.Total)
	}
	if offer.Rows >= base.Rows {
		t.Fatalf("rows %v did not fall; a HAVING qual removes groups", offer.Rows)
	}
	if offer.Rows < 1 {
		t.Fatalf("rows %v below the clamp; clamp_row_est floors at 1", offer.Rows)
	}

	// No quals is a no-op — the unwrapped call path must be byte-identical.
	same := base
	applyHavingQualsToOffer(&same, nil, aggNode, cp)
	if same.Rows != base.Rows || same.Cost != base.Cost {
		t.Fatalf("no-qual call mutated the offer: rows %v cost %+v vs rows %v cost %+v",
			same.Rows, same.Cost, base.Rows, base.Cost)
	}
}
