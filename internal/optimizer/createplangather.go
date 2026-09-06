package optimizer

// createplangather.go — C-19d / P5-04's `createPlan` arms: `create_gather_plan`
// (createplan.c:1913) and `create_gather_merge_plan` (createplan.c:1954).
//
// Both are single-child wrappers and follow `createSortPlan`'s shape, with one
// addition that has no counterpart in PG and is the most important line in the
// file: the child's driving scan must be STAMPED parallel and must EXIST.
//
// Why. PG's worker executes the same plan tree the leader planned, and
// `parallel_aware` was decided per path at path-construction time. goopg's
// `gatherOp.runWorker` instead calls `attachParallelScan` / …Bitmap… / …Index…
// on each worker's freshly built tree — and IGNORES the return value. So a
// subtree those walks do not model does not "stay serial": every worker reads
// the WHOLE relation and the Gather emits N copies of every row. That is a
// wrong-answer bug, so the planner refuses to build the node rather than
// hoping. `drivingScan` (parallel.go) is the planner-side mirror of those
// walks; `stampParallelScan` is its copy-on-write sibling that sets the
// EXPLAIN label on the same scan. Using both here — rather than a private
// second traversal — is what keeps a path-model Gather and a post-pass Gather
// labelling the identical node (rule #2: sibling paths must agree).
//
// Design: docs/design/planner-c19d-gather-paths/DESIGN.md §7.

import "fmt"

// createGatherPlan is `create_gather_plan` (createplan.c:1913): recurse into
// the one child, mark its driving scan parallel, and wrap it in the executor's
// `*Gather` planning `subpath->parallel_workers` workers.
//
// The child's `outputLayout` passes through UNCHANGED, and that is the whole of
// a Gather's coordinate story: it interleaves rows from several workers and
// never touches columns, so output column i is still the child's column i —
// the same statement `createSortPlan` makes about a sort.
func createGatherPlan(p *Path) (Node, outputLayout) {
	child, layout, workers := gatherChildPlan(p, "PathGather")
	return &Gather{pos: child.Pos(), Child: child, WorkersPlanned: workers}, layout
}

// createGatherMergePlan is `create_gather_merge_plan` (createplan.c:1954): the
// same child preparation, plus the ORDERING the leader's merge preserves.
//
// The keys are translated exactly as `createSortPlan` translates a sort's:
// `PathKey.Expr` is written in pre-search BINDING coordinates while the
// executor's `*GatherMerge` evaluates them against its child's row, and
// `PathKey.SortAsc` is ascending-true while `SortKey.Desc` is descending-true.
// Upstream ERRORs when the subpath does not already deliver the pathkeys
// ("gather merge input not sufficiently sorted"); here the path's keys ARE the
// subpath's by construction (makeGatherMergePath copies them), so the
// equivalent failure is an empty list, which panics as a producer bug.
func createGatherMergePlan(p *Path) (Node, outputLayout) {
	if len(p.Pathkeys) == 0 {
		panic("createPlan: PathGatherMerge with no pathkeys; a merge that preserves no ordering is a plain Gather")
	}
	child, layout, workers := gatherChildPlan(p, "PathGatherMerge")
	var index map[int]int
	if layout != nil {
		index = layout.bindingIndex()
	}
	keys := make([]SortKey, len(p.Pathkeys))
	for i, pk := range p.Pathkeys {
		if pk.Expr == nil {
			panic(fmt.Sprintf("createPlan: PathGatherMerge pathkey %d has no expression", i))
		}
		e := pk.Expr
		if index != nil {
			e = translateToLayout("gather merge key", e, layout, index)
		}
		keys[i] = SortKey{Expr: e, Desc: !pk.SortAsc, NullsFirst: pk.NullsFirst}
	}
	return &GatherMerge{pos: child.Pos(), Child: child, WorkersPlanned: workers, Keys: keys}, layout
}

// gatherChildPlan is the preparation both arms share: build the one child,
// stamp its driving scan, and report the worker count.
//
// Every refusal is a panic, per createplan.go's contract — a path that reached
// here in one of these states is a PRODUCER bug, and the alternative to
// panicking is a plan that returns duplicated rows:
//
//   - not exactly one child, or a child that built no node;
//   - `subpath->parallel_workers == 0`, which upstream would turn into a
//     `single_copy` Gather; goopg's producers never offer one and
//     `Gather.SingleCopy` is documented as reserved (makeGatherPath refuses it
//     first, so this is the second line of defence);
//   - a built subtree with no driving scan — the `attachParallelScan` mirror
//     described in the file header. Note the check runs on the STAMPED tree
//     and uses `drivingScan`, the same predicate `stampParallelScan` walks, so
//     "nothing was stamped" and "nothing will be attached" are one question
//     asked once.
func gatherChildPlan(p *Path, what string) (Node, outputLayout, int) {
	if len(p.Children) != 1 || p.Children[0] == nil {
		panic(fmt.Sprintf("createPlan: %s with %d children, want exactly 1", what, len(p.Children)))
	}
	sub := p.Children[0]
	workers := sub.ParallelWorkers
	if workers <= 0 {
		panic(fmt.Sprintf("createPlan: %s over a subpath planning %d workers; the single_copy shape is not modelled", what, workers))
	}
	child, layout := createPlanNode(sub)
	if child == nil {
		panic(fmt.Sprintf("createPlan: %s over a child path that built no node", what))
	}
	stamped := stampParallelScan(child)
	if drivingScan(stamped) == nil {
		panic(fmt.Sprintf(
			"createPlan: %s over a subtree with no driving scan; every worker would read the whole relation and the Gather would return %d+1 copies of every row",
			what, workers))
	}
	return stamped, layout, workers
}
