package optimizer

import "fmt"

// create_plan — turning a chosen Path into the executor Nodes goopg already runs.
// See docs/design/cost-model/ chapter 03 §3. This is the one place goopg's Path
// model deliberately stops short of PostgreSQL's: PG's create_plan
// (createplan.c:337) emits a fresh Plan IR; goopg's translates to its existing
// planner.Node types, leaving the executor untouched.
//
// Phase C0.2 establishes the SEAM, not the full translation. Path generation does
// not exist yet (it arrives in C3/C4), so the only kind constructed today is
// PathPrebuilt — the bridge that wraps the join subtree the integer DP already
// builds (design ch. 03 §3.1 staging note). createPlan on a PathPrebuilt returns
// that subtree unchanged, so routing the DP's output through it is provably
// plan-preserving. The real per-kind arms below are unreachable until C3/C4
// generate those paths; they panic loudly rather than silently mis-build, because
// a constructed-but-unhandled kind would be a bug in the phase that adds it.

// createPlan translates a chosen Path into an executor Node. Its later
// responsibilities (design ch. 03 §3) — inserting Sort nodes implied by pathkeys,
// reconstructing scan detail, threading nested-loop parameters — attach to the
// real path kinds as those kinds are introduced. In C0 the wrapped subtree
// already carries every such detail, so createPlan is an unwrap.
func createPlan(p *Path) Node {
	n, _ := createPlanNode(p)
	return n
}

// createPlanNode is createPlan with the coordinate map the join arms need
// (M0127-P5.5-e-i). It returns the emitted node together with its
// `outputLayout` — for each output column, the pre-search binding coordinate it
// came from — because a join is the first arm that merges two schemas and
// therefore the first that must re-base its quals onto the merged row
// (createplanjoin.go explains why the alternative silently reads wrong
// columns).
//
// The layout is threaded through the recursion rather than recomputed at each
// node, so it cannot drift from the tree: it is built where the schema is built,
// from the same children, in the same statement. `createPlan` above is the entry
// point for every caller that only wants the node; the layout leaves this
// package only once P5.5-f composes 03 §10's boundary map from the search
// root's.
func createPlanNode(p *Path) (Node, outputLayout) {
	n, lay := createPlanNodeUnpriced(p)
	// take2 P0-02: stamp the winning path's cost onto the node it produced.
	// This is the single funnel every search-produced node passes through, so
	// a new path kind cannot forget to carry its cost forward — which is why
	// the stamp lives here rather than in each arm.
	stampPlanCost(n, p)
	return n, lay
}

func createPlanNodeUnpriced(p *Path) (Node, outputLayout) {
	if p == nil {
		return nil, nil
	}
	switch p.Kind {
	case PathPrebuilt:
		// The DP-built subtree, carried whole. Nothing to reconstruct — and
		// its coordinates are known only when it is a level-1 leaf
		// (`buildInitialRels`); the C0 bridge's join subtree was laid out by
		// the integer DP and has no relationship to this map.
		return p.node, baseRelLayout(p.Rel, p.node)
	case PathIndexScan:
		// The first real arm (M0127-P5.5-c): the probe the search costed,
		// rebuilt from the carrier P5.5-a/-b landed on the path and the leaf
		// `buildInitialRels` recorded on the rel. createplanindex.go.
		n := createIndexScanPlan(p)
		return n, baseRelLayout(p.Rel, n)
	case PathSeqScan:
		// The index arm's mirror (M0127-P5.5-d): the same leaf resolver with
		// the probe machinery removed. createplansimple.go.
		n := createSeqScanPlan(p)
		return n, baseRelLayout(p.Rel, n)
	case PathSort:
		// The first arm with a child path (M0127-P5.5-d): P5.4c's merge paths
		// carry Sort children, so this must exist before the join arms do.
		// createplansimple.go.
		return createSortPlan(p)
	case PathHashJoin:
		// The first join arm (M0127-P5.5-e-i) and the first consumer of the
		// layout above. createplanjoin.go.
		return createHashJoinPlan(p)
	case PathMergeJoin:
		// The second join arm (M0127-P5.5-e-ii-a): the same prologue, plus the
		// ORDERED key list a merge sorts by and the absorption of the explicit
		// `PathSort` children goopg's merge operator makes redundant.
		// createplanjoin.go.
		return createMergeJoinPlan(p)
	case PathNestLoop:
		// The third join arm (M0127-P5.5-e-ii-b), and the only one that BINDS a
		// parameter: it emits a plain `*Join` or a `*NestedLoopIndexJoin`
		// depending on whether the inner child is parameterised, and the two
		// shapes translate their expressions onto DIFFERENT layouts — the probe
		// keys onto the outer alone, the residual onto the merged row.
		// createplannl.go.
		return createNestLoopPlan(p)
	case PathGather:
		// C-19d (P5-04): the Gather the SEARCH chose, priced by cost_gather
		// and compared against the serial path by add_path — as opposed to
		// the one `MaybeAddGather` stamps onto a finished tree. Both shapes
		// exist while C-19h is open; the post-pass declines on a tree that
		// already carries one (parallel.go). createplangather.go.
		return createGatherPlan(p)
	case PathGatherMerge:
		// The order-preserving twin, priced by cost_gather_merge.
		return createGatherMergePlan(p)
	case PathMemoize:
		// A Memoize path has NO arm here, deliberately (M0127-P5.4b-ii-b-2).
		// goopg's executor expresses the cache as `NestedLoopIndexJoin.InnerMemo`
		// — a field on the join, not a node between the join and its inner —
		// so the only legal consumer is `createNestLoopIndexJoinPlan`, which
		// unwraps it and rebuilds the probe below it. Reaching this arm means a
		// Memoize path escaped into a position where nothing will bind its
		// parameter, and emitting the bare child there would silently drop the
		// cache the search costed.
		panic("createPlan: PathMemoize outside a nested-loop inner; goopg's cache is NestedLoopIndexJoin.InnerMemo and has no free-standing node")
	case PathBitmapHeapScan:
		// M0128-P2.4: bitmap heap scan — the page-at-a-time heap visitor.
		n, err := createBitmapHeapScanPlan(p)
		if err != nil {
			// Was `return nil, nil`, which turned a construction failure into a
			// nil plan node and a confusing error much later. A failure here is a
			// producer bug, the same class every other createPlan arm panics on.
			panic("createPlan: PathBitmapHeapScan: " + err.Error())
		}
		return n, baseRelLayout(p.Rel, n)
	case PathBitmapIndexScan:
		// M0128-P2.4: bitmap index scan — the TIDBitmap producer leaf.
		n := createBitmapIndexScanPlan(p)
		return n, baseRelLayout(p.Rel, n)
	case PathBitmapAnd:
		// M0128-P2.4: bitmap intersection combinator.
		n := buildBitmapAndOrPlan(p, true)
		return n, baseRelLayout(p.Rel, n)
	case PathBitmapOr:
		// M0128-P2.4: bitmap union combinator.
		n := buildBitmapAndOrPlan(p, false)
		return n, baseRelLayout(p.Rel, n)
	case PathAgg:
		// C-15: the GROUP_AGG upper rel's candidate. createplansimple.go.
		return createAggPlan(p)
	case PathDistinct:
		// C-16: the DISTINCT upper rel's candidate. createplansimple.go.
		return createDistinctPlan(p)
	case PathWindow:
		// C-18: the WINDOW upper rel's candidate. createplansimple.go.
		return createWindowPlan(p)
	case PathSetOp:
		// C-18: the SETOP upper rel's candidate — the only two-input arm
		// above the seam. createplansimple.go.
		return createSetOpPlan(p)
	default:
		// PathMultiHash / … do not have arms yet. Reaching here means a phase
		// constructed a path kind without teaching createPlan to translate it.
		panic(fmt.Sprintf("createPlan: path kind %d not yet translatable (P5.5)", p.Kind))
	}
}

// createPlanFromDPChoice expresses the integer DP's chosen join subtree as a
// (prebuilt) Path and rebuilds it through createPlan. In C0 this is an identity
// transform over the Node — its purpose is to establish the single seam through
// which, from C4, the *cost-selected* path will flow instead of the integer DP's
// hand-built tree. Wiring it now (plan-preserving) means C4 swaps what feeds this
// seam, not the integration itself.
func createPlanFromDPChoice(n Node) Node {
	if n == nil {
		return nil
	}
	return createPlan(newPrebuiltPath(&RelOptInfo{}, n))
}
