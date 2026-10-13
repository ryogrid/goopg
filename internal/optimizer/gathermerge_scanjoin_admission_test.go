package optimizer

import "testing"

// TestScanJoinRelDropsATiedGatherMerge pins M0146-0190 with TPC-DS Q26's
// SF0.25 numbers (PG 18.3, OPTIMIZER_DEBUG pathlist dump).
//
// PG builds the worker-sort Gather Merge on the topmost scan/join rel
// (generate_useful_gather_paths, override_rows false), where it meets the
// search's Nested Loop over a lower Gather Merge: same ordering, same rows,
// fuzz-equal cost (14285.24..14292.59). The new path is cheaper in total but
// starts later, and the rel considers startup under the LIMIT, so add_path's
// tight-fuzz tie-break calls them different and keeps the incumbent. PG's
// grouped rel never aggregates over that Gather Merge and plans Sort over
// Gather. goopg filed the aggregate over it anyway and won the near-tie.
func TestScanJoinRelDropsATiedGatherMerge(t *testing.T) {
	cp := defaultCostParams()
	schema := cpjSchema("a", 2)
	key := []PathKey{{Expr: &ColumnRef{Name: "a0", Index: 0}, SortAsc: true}}

	build := func(considerStartup bool, candStartup float64) (Node, *Path, *Path) {
		rel := fetchUpperRel(newUpperRels(), UpperGroupAgg, 0, 0)
		rel.Rows = 26
		rel.ConsiderStartup = considerStartup
		rel.Pathlist = []*Path{
			{Kind: PathNestLoop, Rel: rel, Rows: 26, Cost: Cost{Startup: 1964.66, Total: 14291.96}},
			{Kind: PathNestLoop, Rel: rel, Rows: 26, Cost: Cost{Startup: candStartup, Total: 14292.59}, Pathkeys: key},
		}
		root := &SeqScan{schema: schema}
		markSearchedTree(root)
		root.setSearchRel(rel)
		seed := &Path{Rows: 26, Cost: Cost{Startup: 2964.66, Total: 14288.90}}
		workerSort := &Path{Kind: PathSort, Rows: 8, Cost: Cost{Startup: 13286.42, Total: 13286.44},
			Pathkeys: key, ParallelSafe: true, ParallelWorkers: 3}
		gm := &Path{Kind: PathGatherMerge, Rows: 25, Pathkeys: key, Children: []*Path{workerSort},
			Cost: gatherMergeCost(cp, workerSort.Cost, 3, 25)}
		return root, seed, gm
	}

	child, seed, gm := build(true, 14285.24)
	if scanJoinRelKeepsGatherMerge(child, seed, gm, cp) {
		t.Errorf("a fuzz-equal, earlier-starting sorted search path must reject the Gather Merge (PG keeps the incumbent)")
	}
	child, seed, gm = build(false, 14285.24)
	if !scanJoinRelKeepsGatherMerge(child, seed, gm, cp) {
		t.Errorf("without startup interest the cheaper-total Gather Merge evicts the incumbent and must be kept")
	}
	child, seed, gm = build(true, 14295.00)
	if !scanJoinRelKeepsGatherMerge(child, seed, gm, cp) {
		t.Errorf("a Gather Merge no worse on both axes must be kept")
	}
	// TPC-DS Q28: the search stamped the same Gather Merge on the scan rel.
	// Upstream's rel holds no gathered path before this call, so it is no
	// incumbent.
	child, seed, gm = build(true, 14285.24)
	rel := searchedJoinInputRelOf(child)
	twin := *gm
	twin.Rel, twin.Rows = rel, 26
	rel.Pathlist = []*Path{&twin}
	if !scanJoinRelKeepsGatherMerge(child, seed, gm, cp) {
		t.Errorf("the search's own Gather Merge is not an incumbent of generate_useful_gather_paths")
	}
	if !scanJoinRelKeepsGatherMerge(&SeqScan{schema: schema}, seed, gm, cp) {
		t.Errorf("an input that is not a searched rel has no pathlist to compete in; the arm must stay")
	}
}
