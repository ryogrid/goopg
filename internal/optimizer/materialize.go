package optimizer

// M0146-0010 — the Materialize path. Until this file the executor materialised
// EVERY streaming nested-loop inner unconditionally (join_nl_stream.go's
// openNestedLoop wrapped o.right in newMaterializeOp) and the cost model
// priced that hidden wrap inside nestLoopInnerRescanCost — a plan-level fact
// expressed nowhere in the plan. This file ports the PG surface whole:
//
//	create_material_path   pathnode.c:1637-1658   → materialInnerPath
//	cost_material          costsize.c:2485-2527   → costMaterial
//	cost_rescan T_Material costsize.c:4703-4729   → pathRescanCost's
//	                                                  PathMaterial arm
//	admission              joinpath.c:1890-1901   → materialInnerPathFor
//	exclusion set          execAmi.c:640-647      → execMaterializesOutput
//
// The coupling that motivated the file: PG elects materialisation per PATH
// and prices the election; goopg must do the same so the *Materialize node a
// moved plan carries is the one the plan actually costed — and so EXPLAIN
// shows `Materialize` exactly where PG shows it.

import (
	"fmt"
	"math"
)

// costMaterial is `cost_material` (costsize.c:2485-2527): the price of one
// full pass through a Material node — the child's own cost plus the
// write-then-read bookkeeping.
//
//	build charge — `2 * cpu_operator_cost * tuples`, "more than what
//	cost_rescan charges for materialize … (the extra cost ensures we'll
//	prefer materializing the smaller rel)" (costsize.c:2496-2505);
//	spill charge — `seq_page_cost * ceil(nbytes / BLCKSZ)` when the buffered
//	result exceeds the memory budget (:2513-2521);
//	startup passes through unchanged — the Material adds no startup of its
//	own (:2489).
//
// `input` is the wrapped path's cost; tuples/avgVarBytes/ncols are its row and
// width figures. `cp.workMem` is the one deliberate divergence: it is the
// hash-multiplier-adjusted budget the rest of this cost model spills against
// (cost_funcs.go), so the Material arm shares the project's width model
// instead of introducing a second one — the same reading pathRescanCost's
// PathSort arm already takes.
func costMaterial(cp costParams, input Cost, tuples, avgVarBytes float64, ncols int) Cost {
	run := input.Total - input.Startup
	run += 2 * cp.cpuOperatorCost * tuples
	if nbytes := relationByteSize(tuples, avgVarBytes, ncols); nbytes > float64(cp.workMem) {
		run += cp.seqPageCost * math.Ceil(nbytes/blockSizeBytes)
	}
	return Cost{Startup: input.Startup, Total: input.Startup + run}
}

// materialRescanCost is `cost_rescan`'s T_Material/T_Sort arm
// (costsize.c:4703-4729): replaying the buffer costs `cpu_operator_cost` per
// tuple, no startup — "even cheaper to rescan than the ones above … they do
// not implement qual filtering or projection" — plus `seq_page_cost` per page
// of re-read when the buffer spilled. pathRescanCost dispatches here for both
// PathMaterial and PathSort so the two cannot drift.
func materialRescanCost(cp costParams, rows, avgVarBytes float64, ncols int) float64 {
	run := cp.cpuOperatorCost * rows
	if nbytes := relationByteSize(rows, avgVarBytes, ncols); nbytes > float64(cp.workMem) {
		run += cp.seqPageCost * math.Ceil(nbytes/blockSizeBytes)
	}
	return run
}

// materialInnerPath is `create_material_path` (pathnode.c:1637-1658): the
// MaterialPath match_unsorted_outer files over `inner_cheapest_total`. Rows,
// width and pathkeys pass through the wrapper unchanged — the buffer replays
// the child's output verbatim, including its ordering (:1649) — and cost
// carries cost_material's build overhead.
//
// The caller decides admission (materialInnerPathFor); this constructor only
// assembles. `Assert(subpath->parent == rel)` upstream is the caller's
// invariant: rel is the inner rel subpath belongs to.
func materialInnerPath(rel *RelOptInfo, sub *Path, cp costParams) *Path {
	return &Path{
		Kind: PathMaterial,
		Rel:  rel,
		Rows: sub.Rows,
		// pathnode.c:1649 — `pathkeys = subpath->pathkeys`: replay preserves
		// order, so the Material advertises the child's ordering upward.
		Pathkeys:    sub.Pathkeys,
		NCols:       sub.NCols,
		AvgVarBytes: sub.AvgVarBytes,
		OutputWidth: sub.OutputWidth,
		Cost:      costMaterial(cp, sub.Cost, sub.Rows, pathAvgVarBytes(sub), pathNCols(sub)),
		// :1647-1648 — `parallel_safe = rel->consider_parallel &&
		// subpath->parallel_safe`; `parallel_workers = subpath->parallel_workers`;
		// `parallel_aware = false`. `Assert(matpath->parallel_safe)` at
		// joinpath.c:2140 is guaranteed by the admission check.
		ParallelSafe:    rel.ConsiderParallel && sub.ParallelSafe,
		ParallelWorkers: sub.ParallelWorkers,
		ParallelAware:   false,
		// :1645 — `param_info = subpath->param_info`. Admission restricts this
		// to unparameterised inners, so this is 0 in practice; carrying the
		// field keeps the construction honest if that ever widens.
		RequiredOuter: sub.RequiredOuter,
		Children:    []*Path{sub},
		// costsize.c:2522 — the input's count plus one when the method is
		// disabled. Admission only files this path when enable_material is
		// on, so the +1 is dead code by construction — carried so a future
		// caller that bypasses the gate still prices honestly.
		DisabledNodes: sub.DisabledNodes + disabledCount(!cp.enableMaterial),
	}
}

func disabledCount(off bool) int {
	if off {
		return 1
	}
	return 0
}

// materialInnerPathFor applies the two-site admission rule before building:
//
//	match_unsorted_outer     joinpath.c:1890-1901 — `if (enable_material &&
//	                         inner_cheapest_total != NULL &&
//	                         !ExecMaterializesOutput(pathtype))`;
//	consider_parallel_nestloop joinpath.c:2129-2141 — same, plus
//	                         `inner_cheapest_total->parallel_safe` and
//	                         `!PATH_PARAM_BY_REL(inner, outerrel)` (the
//	                         caller's own tests cover the second; the third
//	                         is the RequiredOuter != 0 check below), and it is
//	                         skipped entirely under JOIN_UNIQUE_INNER.
//
// A nil return is "PG would file no matpath for this inner". The unique-inner
// and join-method gates live at the call sites, which is where PG puts them.
func materialInnerPathFor(inner *Path, rel *RelOptInfo, cp costParams) *Path {
	if inner == nil || !cp.enableMaterial {
		return nil
	}
	if inner.RequiredOuter != 0 {
		// A parameterised inner cannot be materialised: every outer row
		// supplies different parameters, so the "cache" would be wrong
		// (joinpathsmemoize.go, take2 P2-06 — same reason, kept).
		return nil
	}
	if execMaterializesOutput(inner) {
		return nil
	}
	return materialInnerPath(rel, inner, cp)
}

// execMaterializesOutput is `ExecMaterializesOutput` (execAmi.c:640-647)
// projected onto goopg's PathKind space: node types that return their result
// from stored state — a rescannable buffer — rather than by re-executing.
// Materializing such an inner buys nothing, so PG files no matpath over it.
//
//	goopg kind / node            PG pathtype
//	PathMaterial                 T_Material
//	PathSort                     T_Sort (a tuplesort rescans from store)
//	PathMemoize                  — upstream has no exclusion; listed here
//	                             anyway because a hash cache is by
//	                             construction a materialising wrapper and
//	                             buffering it would double the memory. It
//	                             cannot reach the site (Memoize carries
//	                             RequiredOuter), so the arm is a stated
//	                             no-op, not a silent divergence.
//	*CTEScan                     T_CteScan — cteScanOp fills and replays
//	                             ctx.CTERowCache (executor.go:109-112)
//	*MaterializedCTEScan         T_CteScan's DML twin — reads
//	                             ctx.MaterializedCTEs
//	*WorkTableScan               T_WorkTableScan
//	*ScalarFuncScan/RowsFrom/
//	  GenerateSeries             T_FunctionScan — nodeFunctionscan.c
//	                             "always executes the function to completion
//	                             and caches the results in a tuplestore"
//	                             (costsize.c:4645-4650)
//	*Sort                        T_Sort
//	*Materialize                 T_Material
//
// PG's T_TableFuncScan and T_NamedTuplestoreScan arms have no goopg node —
// the set above is complete against the current node inventory.
func execMaterializesOutput(p *Path) bool {
	if p == nil {
		return false
	}
	switch p.Kind {
	case PathMaterial, PathSort, PathMemoize:
		return true
	}
	return nodeMaterializesOutput(pathLeafNode(p))
}

// pathLeafNode is the executor node class a LEAF path emits, for the
// consumers that must answer PG's pathtype questions (ExecMaterializesOutput,
// cost_rescan) about a leaf: a PathPrebuilt names it directly, and the leaf
// scan kinds draw it from Rel.baseLeaf — a CTE leaf reaches the NL as a
// PathSeqScan over a *CTEScan, which is PG's T_CteScan. Every other kind is
// an interior path (its Rel may still be a base rel — a Unique, Agg or
// Gather over one — but its pathtype is its own), so the answer is nil.
func pathLeafNode(p *Path) Node {
	if p == nil {
		return nil
	}
	switch p.Kind {
	case PathPrebuilt:
		return leafBaseScan(p.node)
	case PathSeqScan, PathIndexScan, PathBitmapHeapScan:
		if p.Rel != nil {
			return leafBaseScan(p.Rel.baseLeaf)
		}
	}
	return nil
}

// isTuplestoreScanNode reports PG's T_CteScan / T_WorkTableScan class —
// scans whose output a rescan re-reads from a tuplestore.
func isTuplestoreScanNode(n Node) bool {
	switch n.(type) {
	case *CTEScan, *MaterializedCTEScan, *WorkTableScan:
		return true
	}
	return false
}

// isFunctionScanNode reports PG's T_FunctionScan class — nodeFunctionscan.c
// runs the function to completion into a tuplestore on the first pass.
func isFunctionScanNode(n Node) bool {
	switch n.(type) {
	case *ScalarFuncScan, *RowsFrom, *GenerateSeries:
		return true
	}
	return false
}

// nodeMaterializesOutput is the plan-node half of execMaterializesOutput for
// PathPrebuilt leaves (whose path kind hides the node class) and for the
// leaf scan kinds (which hide it one level deeper, in Rel.baseLeaf behind
// the filter/labelling wrappers leafBaseScan strips).
func nodeMaterializesOutput(n Node) bool {
	switch n.(type) {
	case *Materialize, *Sort, *MaterializedCTEScan, *WorkTableScan,
		*ScalarFuncScan, *RowsFrom, *GenerateSeries:
		return true
	case *CTEScan:
		// CTEScan's labelling wrapper aside (plan.go:1816), its executor arm
		// always builds cteScanOp, which "materializes all rows on first
		// Open() and replays them on subsequent Open() calls"
		// (executor.go:108-112) — T_CteScan's tuplestore, verbatim.
		return true
	}
	return false
}

// createMaterialPlan is `make_material` (createplan.c) — the PathMaterial arm
// of createPlan: build the child, wrap it. The node adds no quals, no
// projection and no ordering of its own — "the only content is 'rescanning
// me is cheap'" — so the child's output layout passes through untouched, the
// same convention createSortPlan uses.
func createMaterialPlan(p *Path) (Node, outputLayout) {
	if len(p.Children) != 1 || p.Children[0] == nil {
		panic(fmt.Sprintf("createPlan: PathMaterial with %d children, want exactly 1", len(p.Children)))
	}
	child, childLayout := createPlanNode(p.Children[0])
	if child == nil {
		panic("createPlan: PathMaterial over a child path that built no node")
	}
	return &Materialize{pos: child.Pos(), Child: child}, childLayout
}
