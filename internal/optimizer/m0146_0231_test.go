package optimizer

import "testing"

// M0146-0231: add_path_precheck rejects a path a complete path dominates on
// pathkeys and both costs without consulting parallel safety, so a costlier
// parallel-safe join never reaches add_path's parallel_safe rule (TPC-DS
// SF0.25 Q64: PG's (cs_ui, item) joinrel keeps one path).
func TestAddPathPrecheckIgnoresParallelSafety(t *testing.T) {
	rel := newRelOptInfo(RelSet(3), 1, 8)
	rel.Pathlist = []*Path{{Kind: PathNestLoop, Rel: rel, Rows: 1, Cost: Cost{Startup: 13812, Total: 13873.5}}}

	// A faster-starting, costlier path: rejected while startup is not
	// interesting (consider_startup false)...
	if addPathPrecheck(rel, 0, 1.02, 42064.7, nil, 0) {
		t.Fatalf("precheck admitted a path dominated on total with consider_startup off")
	}
	// ...kept when it is.
	rel.ConsiderStartup = true
	if !addPathPrecheck(rel, 0, 1.02, 42064.7, nil, 0) {
		t.Fatalf("precheck rejected a path that wins on startup with consider_startup on")
	}
	// A cheaper total always passes.
	rel.ConsiderStartup = false
	if !addPathPrecheck(rel, 0, 20000, 13000, nil, 0) {
		t.Fatalf("precheck rejected a path cheaper on total")
	}
	// Fewer disabled nodes than the incumbent: the incumbent cannot dominate.
	rel.Pathlist[0].DisabledNodes = 1
	if !addPathPrecheck(rel, 0, 1.02, 42064.7, nil, 0) {
		t.Fatalf("precheck let a path with more disabled nodes dominate")
	}
}

// M0146-0231: add_partial_path_precheck compares a partial path against the
// complete paths too — a partial join whose total already loses to a
// complete path, before the Gather is even charged, is rejected.
func TestAddPartialPathPrecheckComparesCompletePaths(t *testing.T) {
	rel := newRelOptInfo(RelSet(3), 1, 8)
	rel.ConsiderParallel = true
	rel.Pathlist = []*Path{{Kind: PathNestLoop, Rel: rel, Rows: 1, Cost: Cost{Startup: 13812, Total: 13873.5}}}
	if addPartialPathPrecheck(rel, 0, 43581.6, nil) {
		t.Fatalf("partial precheck admitted a path a complete path dominates")
	}
	if !addPartialPathPrecheck(rel, 0, 5000, nil) {
		t.Fatalf("partial precheck rejected a path cheaper than every complete path")
	}
	// Clearly better than a comparable partial path: accepted outright.
	rel.PartialPathlist = []*Path{{Kind: PathHashJoin, Rel: rel, Rows: 1, Cost: Cost{Total: 90000}, ParallelWorkers: 2}}
	if !addPartialPathPrecheck(rel, 0, 43581.6, nil) {
		t.Fatalf("partial precheck rejected a path clearly better than the partial incumbent")
	}
	// Clearly worse than a comparable partial path: rejected outright.
	rel.PartialPathlist[0].Cost.Total = 100
	if addPartialPathPrecheck(rel, 0, 5000, nil) {
		t.Fatalf("partial precheck admitted a path clearly worse than the partial incumbent")
	}
}

// M0146-0231: a gather path is never parallel-safe, nor is anything above
// one — a derived leaf whose plan holds a Gather Merge offers no
// parallel-safe path, while the same leaf without it does.
func TestPlanHasGatherNodeSeesThroughSubqueryWrappers(t *testing.T) {
	scan := &SeqScan{}
	agg := &Aggregate{Child: NewGatherMerge(0, &Sort{Child: scan}, 2, nil)}
	leaf := &SubqueryScan{Child: &Filter{Child: agg}}
	if !planHasGatherNode(leaf) {
		t.Fatalf("planHasGatherNode missed a Gather Merge under SubqueryScan/Filter/Aggregate")
	}
	if planHasGatherNode(&SubqueryScan{Child: &Filter{Child: &Aggregate{Child: scan}}}) {
		t.Fatalf("planHasGatherNode reported a Gather in a gather-free leaf")
	}
}

// M0146-0231: a grouping candidate carries create_agg_path's parallel_safe,
// so add_path keeps a costlier parallel-safe GroupAggregate beside a cheaper
// one over a Gather Merge — the path an enclosing partial merge join uses.
func TestAddAggPathKeepsParallelSafeAlternative(t *testing.T) {
	grouped := newRelOptInfo(0, 7, 8)
	grouped.ConsiderParallel = true
	unsafeIn := &Path{Kind: PathPrebuilt, Rel: grouped, Rows: 22, Cost: Cost{Startup: 13811, Total: 13814}}
	safeIn := &Path{Kind: PathPrebuilt, Rel: grouped, Rows: 22, Cost: Cost{Startup: 0.72, Total: 42005}, ParallelSafe: true}
	keys := []PathKey{{Expr: &ColumnRef{Index: 0, Name: "k"}, SortAsc: true}}
	addAggPath(grouped, &Path{Kind: PathAgg, Rel: grouped, Rows: 7, Cost: Cost{Startup: 13811, Total: 13815}, Pathkeys: keys, Children: []*Path{unsafeIn}}, "test")
	addAggPath(grouped, &Path{Kind: PathAgg, Rel: grouped, Rows: 7, Cost: Cost{Startup: 0.72, Total: 42006}, Pathkeys: keys, Children: []*Path{safeIn}}, "test")
	if len(grouped.Pathlist) != 2 {
		t.Fatalf("grouped rel kept %d paths, want the cheaper unsafe and the parallel-safe one", len(grouped.Pathlist))
	}
	safe := 0
	for _, p := range grouped.Pathlist {
		if p.ParallelSafe {
			safe++
		}
	}
	if safe != 1 {
		t.Fatalf("grouped rel holds %d parallel-safe paths, want 1", safe)
	}
}
