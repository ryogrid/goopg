package planner

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
	default:
		// PathNestLoop / PathMultiHash / … do not have arms yet (the nested
		// loops are P5.5-e-ii-b). Reaching here means a phase constructed a
		// path kind without teaching createPlan to translate it.
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
