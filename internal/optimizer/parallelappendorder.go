package optimizer

import (
	"sort"

	"github.com/goopg/goopg/internal/parser"
)

// M0146-0005ac — Parallel Append arm order (`create_append_path`,
// pathnode.c:1343-1361).
//
// PG sorts a parallel-aware Append's subpaths before it stores them: the
// non-partial subpaths by DESCENDING total cost ("the total time to finish
// all non-partial paths is minimized"), then the partial subpaths by
// DESCENDING startup cost, total cost breaking startup ties
// (`append_startup_cost_compare` → `compare_path_costs(..., STARTUP_COST)`),
// relids breaking exact ties. The non-partial list comes first
// (`first_partial_path`). The order is not cosmetic: the executor hands the
// non-partial subplans out in list order, and EXPLAIN prints it — TPC-DS
// Q71's `Parallel Append` lists store, catalog, web (17937 > 12953 > 7759)
// where goopg printed the written web, catalog, store.
//
// goopg's union is a LEFT-DEEP chain of two-child `*SetOp` links, one per
// UNION ALL keyword, which EXPLAIN renders as one flat Append. Each link's
// partial path is priced over its own two children, so the sort cannot run
// inside a link; it runs here, once the top link's plan exists: the
// flattened arms of every parallel-aware UNION ALL link beneath it are
// collected together with the paths that built them, sorted with PG's
// comparator, and the chain is rebuilt left-deep in that order. The
// reorder is row-neutral — a UNION ALL is an unordered multiset and the
// parallel executor already interleaves its arms — and each arm keeps its
// own claimed-whole mark.
//
// Relids ties keep the written order: appendrel children are numbered in
// written order, so PG's `bms_compare` on them ascends exactly as a stable
// sort leaves them.

// parallelAppendArm is one flattened child of a parallel-aware UNION ALL
// chain: the node the link built, the path that built it, and whether the
// link claimed it whole (PG's pa_nonpartial_subpaths membership).
type parallelAppendArm struct {
	node       Node
	path       *Path
	nonPartial bool
}

// flattensIntoParallelAppend reports whether link n is a link of the one
// Append EXPLAIN renders: a parallel-aware, non-merging UNION ALL.
func flattensIntoParallelAppend(n *SetOp) bool {
	return n != nil && n.ParallelAware && n.All && n.Op == parser.SetOpUnion && len(n.MergeKeys) == 0
}

// collectParallelAppendArms walks the chain under link n (built from path
// p) and returns its arms: an inner link of the same Append contributes its
// recorded arms, anything else is an arm, whole.
//
// An inner link was built — and so already ordered — before this one: its
// createSetOpPlan call ran orderParallelAppendArms on it and left its
// flattened arms in `appendArms`. Those are reused as they stand, because
// after a reorder the inner link's node children no longer line up with
// its path's children by position.
func collectParallelAppendArms(p *Path, n *SetOp) []parallelAppendArm {
	var arms []parallelAppendArm
	if inner, ok := n.Left.(*SetOp); ok && flattensIntoParallelAppend(inner) && inner.appendArms != nil {
		arms = append(arms, inner.appendArms...)
	} else {
		arms = append(arms, parallelAppendArm{node: n.Left, path: p.Children[0], nonPartial: n.LeftNonPartial})
	}
	return append(arms, parallelAppendArm{node: n.Right, path: p.Children[1], nonPartial: n.RightNonPartial})
}

// parallelAppendArmLess is create_append_path's ordering: non-partial arms
// first, by total cost descending; partial arms by startup descending, then
// total descending.
func parallelAppendArmLess(a, b parallelAppendArm) bool {
	if a.nonPartial != b.nonPartial {
		return a.nonPartial
	}
	if a.nonPartial {
		return a.path.Cost.Total > b.path.Cost.Total
	}
	if a.path.Cost.Startup != b.path.Cost.Startup {
		return a.path.Cost.Startup > b.path.Cost.Startup
	}
	return a.path.Cost.Total > b.path.Cost.Total
}

// orderParallelAppendArms applies create_append_path's sort to the chain
// whose top link `top` was just built from path p, returning the rebuilt top
// link (or top itself when the order already holds or the chain is not a
// parallel Append). Every rebuilt link pins the chain's original output
// schema: a set operation's column names are the written first arm's, and
// the sort may move a different arm to the front.
func orderParallelAppendArms(p *Path, top *SetOp) *SetOp {
	if !flattensIntoParallelAppend(top) || len(p.Children) != 2 {
		return top
	}
	arms := collectParallelAppendArms(p, top)
	for _, a := range arms {
		if a.node == nil || a.path == nil {
			return top
		}
	}
	sorted := append([]parallelAppendArm(nil), arms...)
	sort.SliceStable(sorted, func(i, j int) bool { return parallelAppendArmLess(sorted[i], sorted[j]) })
	same := true
	for i := range arms {
		if arms[i].node != sorted[i].node {
			same = false
			break
		}
	}
	if same {
		top.appendArms = arms
		return top
	}
	schema := top.Output()
	var cur Node = sorted[0].node
	for k := 1; k < len(sorted); k++ {
		link := *top
		if k < len(sorted)-1 {
			// Inner links are plan-only: the search that read the tag is
			// over, and only the top link is what createSetOpPaths returned.
			link.setOpBranchTag = setOpBranchTag{}
		}
		link.Left = cur
		link.Right = sorted[k].node
		link.LeftNonPartial = k == 1 && sorted[0].nonPartial
		link.RightNonPartial = sorted[k].nonPartial
		link.pinnedSchema = schema
		link.appendArms = nil
		cur = &link
	}
	res := cur.(*SetOp)
	res.appendArms = sorted
	return res
}
