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
	if p == nil {
		return nil
	}
	switch p.Kind {
	case PathPrebuilt:
		// The DP-built subtree, carried whole. Nothing to reconstruct.
		return p.node
	case PathIndexScan:
		// The first real arm (M0127-P5.5-c): the probe the search costed,
		// rebuilt from the carrier P5.5-a/-b landed on the path and the leaf
		// `buildInitialRels` recorded on the rel. createplanindex.go.
		return createIndexScanPlan(p)
	default:
		// PathSeqScan / PathHashJoin / … do not have arms yet (they are the
		// remainder of P5.5). Reaching here means a phase constructed a path
		// kind without teaching createPlan to translate it.
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
