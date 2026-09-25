package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestSetOpBranchRelOfCarriesNestedSetOp pins M0144-0003b-1's core: a link of
// a left-deep UNION ALL chain answers with ITS SETOP rel, so the link above
// can see it. Before the carry `searchedRelOf` stopped at the `*SetOp` — two
// boundary children — and the outer link read nil.
func TestSetOpBranchRelOfCarriesNestedSetOp(t *testing.T) {
	inner := setOpTestNode(parser.SetOpUnion, true,
		upperOrderedInput(1000), upperOrderedInput(400))
	rel := &RelOptInfo{Relids: 7}

	if got := setOpBranchRelOf(inner); got != nil {
		t.Fatalf("unstamped *SetOp: setOpBranchRelOf = %v, want nil (pre-carry behaviour)", got)
	}

	stampSetOpBranchRel(inner, rel)
	if got := setOpBranchRelOf(inner); got != rel {
		t.Fatalf("stamped *SetOp: setOpBranchRelOf = %v, want the carried rel", got)
	}
}

// TestSetOpBranchRelOfThroughWrapper pins that the walk still descends the
// boundary chain to reach a carrier, the way it does for a searched root.
func TestSetOpBranchRelOfThroughWrapper(t *testing.T) {
	inner := setOpTestNode(parser.SetOpUnion, true,
		upperOrderedInput(1000), upperOrderedInput(400))
	rel := &RelOptInfo{Relids: 7}
	stampSetOpBranchRel(inner, rel)

	wrapped := &Project{Child: inner}
	if got := setOpBranchRelOf(wrapped); got != rel {
		t.Fatalf("*Project over a stamped *SetOp: setOpBranchRelOf = %v, want the carried rel", got)
	}
}

// TestSetOpBranchRelOfPrefersOutermostCarrier pins the ordering rule on
// setOpBranchRelOf: a *Gather over a partial *SetOp is BOTH a carrier and a
// single-child boundary wrapper, and the parent holds the Gather — so the
// Gather's own rel is the answer, not the one below it.
func TestSetOpBranchRelOfPrefersOutermostCarrier(t *testing.T) {
	innerRel := &RelOptInfo{Relids: 3}
	outerRel := &RelOptInfo{Relids: 9}
	inner := setOpTestNode(parser.SetOpUnion, true,
		upperOrderedInput(1000), upperOrderedInput(400))
	stampSetOpBranchRel(inner, innerRel)
	g := &Gather{Child: inner}
	stampSetOpBranchRel(g, outerRel)

	if got := setOpBranchRelOf(g); got != outerRel {
		t.Fatalf("setOpBranchRelOf = %v, want the *Gather's own rel %v", got, outerRel)
	}
}

// TestSetOpBranchRelOfKeepsSearchedRoots pins that widening the accessor by
// one terminus did not change the answer for the case it already served.
func TestSetOpBranchRelOfKeepsSearchedRoots(t *testing.T) {
	rel := &RelOptInfo{Relids: 5}
	n := &searchedPricedNode{pricedNode: *upperOrderedInput(1000)}
	n.markFromJoinSearch()
	n.setSearchRel(rel)

	if got := setOpBranchRelOf(n); got != rel {
		t.Fatalf("searched root: setOpBranchRelOf = %v, want the searched rel", got)
	}
	if got, want := setOpBranchRelOf(n), searchedRelOf(n); got != want {
		t.Fatalf("setOpBranchRelOf = %v, searchedRelOf = %v — must agree on a searched root", got, want)
	}
}

// TestSetOpBranchPartialChainOKAcceptsCarrier pins the second half of the
// carry: the chain predicate must treat a carrying set operation as an
// admissible terminus, or the rel would be threaded and then refused.
func TestSetOpBranchPartialChainOKAcceptsCarrier(t *testing.T) {
	inner := setOpTestNode(parser.SetOpUnion, true,
		upperOrderedInput(1000), upperOrderedInput(400))

	if setOpBranchPartialChainOK(inner) {
		t.Fatal("unstamped *SetOp: chain OK = true, want false (pre-carry behaviour)")
	}
	stampSetOpBranchRel(inner, &RelOptInfo{Relids: 7})
	if !setOpBranchPartialChainOK(inner) {
		t.Fatal("stamped *SetOp: chain OK = false, want true")
	}
	if !setOpBranchPartialChainOK(&Project{Child: inner}) {
		t.Fatal("*Project over a stamped *SetOp: chain OK = false, want true")
	}
}

// TestPartialSetOpPathComposesChain is the end-to-end shape M0144-0003b-1
// exists for: the outer link of a three-branch UNION ALL files a partial
// path whose left subpath is the inner link's own partial SetOp path. PG
// reaches the same plan without a chain at all — `pull_up_simple_union_all`
// makes the union one three-child appendrel.
func TestPartialSetOpPathComposesChain(t *testing.T) {
	cp := defaultCostParams()

	innerRel := &RelOptInfo{ConsiderParallel: true, PartialPathlist: []*Path{{
		Kind: PathSetOp, Rows: 700, Cost: Cost{Startup: 1, Total: 70},
		ParallelSafe: true, ParallelWorkers: 2,
	}}}
	inner := setOpTestNode(parser.SetOpUnion, true,
		upperOrderedInput(1000), upperOrderedInput(400))
	stampSetOpBranchRel(inner, innerRel)

	rightRel := &RelOptInfo{ConsiderParallel: true, PartialPathlist: []*Path{{
		Kind: PathSeqScan, Rows: 200, Cost: Cost{Startup: 2, Total: 20},
		ParallelSafe: true, ParallelWorkers: 3,
	}}}
	r := &searchedPricedNode{pricedNode: *upperOrderedInput(200)}
	r.markFromJoinSearch()

	outer := setOpTestNode(parser.SetOpUnion, true, inner, r)
	outerRel := &RelOptInfo{LeftBranchRel: innerRel, RightBranchRel: rightRel}

	addPartialSetOpPath(outerRel, outer, cp, false)

	if len(outerRel.PartialPathlist) != 1 {
		t.Fatalf("outer link PartialPathlist = %d entries, want 1 (the chain composes)",
			len(outerRel.PartialPathlist))
	}
	if got := outerRel.PartialPathlist[0].Kind; got != PathSetOp {
		t.Fatalf("filed path Kind = %v, want PathSetOp", got)
	}
}
