package optimizer

import "testing"

// TestScanJoinRelDropsATiedGather pins M0146-0219 with TPC-DS Q61's SF0.25
// numbers, which an add_path probe in a PG 18.3 build logged on the topmost
// scan/join rel.
//
// generate_gather_paths files the simple Gather of the cheapest partial path
// (17.56..16964.64, 3 workers) on that rel with the rel's own rows (override_rows
// false): 1017.56..17964.74. The rel already holds the search's serial Nested
// Loop over a lower Gather, 1002.48..18131.32. The totals are within 1%, and
// the incumbent starts more than 1% earlier, so add_path keeps the incumbent
// whether or not the rel considers startup. PG's grouped rel therefore never
// aggregates over that Gather and plans Finalize Aggregate -> Gather -> Partial
// Aggregate. goopg's `Aggregate -> Gather` arm used to compete anyway and won
// by a few hundredths.
func TestScanJoinRelDropsATiedGather(t *testing.T) {
	cp := defaultCostParams()
	schema := cpjSchema("a", 2)

	build := func(considerStartup bool, serialTotal float64) (Node, *Path, *Path) {
		rel := fetchUpperRel(newUpperRels(), UpperGroupAgg, 0, 0)
		rel.Rows = 1
		rel.ConsiderStartup = considerStartup
		rel.Pathlist = []*Path{
			{Kind: PathNestLoop, Rel: rel, Rows: 1, Cost: Cost{Startup: 1002.48, Total: serialTotal}},
		}
		root := &SeqScan{schema: schema}
		markSearchedTree(root)
		root.setSearchRel(rel)
		seed := &Path{Rows: 1, Cost: Cost{Startup: 1002.48, Total: serialTotal}}
		pseed := &Path{Kind: PathNestLoop, Rows: 1, Cost: Cost{Startup: 17.56, Total: 16964.64},
			ParallelSafe: true, ParallelWorkers: 3}
		// The arm's own Gather is sized by compute_gather_rows (3); the
		// probe re-prices it at the rel's rows, as upstream's call does.
		g := &Path{Kind: PathGather, Rows: 3, ParallelWorkers: 3, Children: []*Path{pseed},
			Cost: gatherCost(cp, pseed.Cost, 3)}
		return root, seed, g
	}

	for _, considerStartup := range []bool{false, true} {
		child, seed, g := build(considerStartup, 18131.32)
		if scanJoinRelKeepsGather(child, seed, g, cp) {
			t.Errorf("considerStartup=%v: a fuzz-equal, earlier-starting serial path must reject the Gather", considerStartup)
		}
		// The Gather Merge twin skips candidates without pathkeys, since they
		// cannot dominate an ordered path. That is why the plain Gather needs
		// its own check.
		gm := &Path{Kind: PathGatherMerge, Rows: 3, Children: g.Children,
			Pathkeys: []PathKey{{Expr: &ColumnRef{Name: "a0", Index: 0}, SortAsc: true}}, Cost: g.Cost}
		if !scanJoinRelKeepsGatherMerge(child, seed, gm, cp) {
			t.Errorf("considerStartup=%v: an unordered incumbent cannot reject an ordered Gather Merge", considerStartup)
		}
	}
	// More than 1% cheaper in total: the Gather is kept. Without startup
	// interest it dominates the incumbent; with it, the two are different.
	for _, considerStartup := range []bool{false, true} {
		child, seed, g := build(considerStartup, 18300)
		if !scanJoinRelKeepsGather(child, seed, g, cp) {
			t.Errorf("considerStartup=%v: a Gather more than 1%% cheaper in total must be kept", considerStartup)
		}
	}
	// A gathered path the search stamped on the rel is no incumbent.
	child, seed, g := build(true, 18131.32)
	rel := searchedJoinInputRelOf(child)
	twin := *g
	twin.Rel, twin.Rows = rel, 1
	rel.Pathlist = []*Path{&twin}
	if !scanJoinRelKeepsGather(child, seed, g, cp) {
		t.Errorf("the search's own Gather is not an incumbent of generate_gather_paths")
	}
	if !scanJoinRelKeepsGather(&SeqScan{schema: schema}, seed, g, cp) {
		t.Errorf("an input that is not a searched rel has no pathlist to compete in; the arm must stay")
	}
}
