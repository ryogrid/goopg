package optimizer

// M0127-P5.5-f-ii-a — the searched subtree is OPAQUE to the legacy
// layout-correction family, and the proof that it needs nothing from it.
//
// Design: leftdeep-joins 03 §10, 02 §3. Companion of `createplanroot.go`
// (P5.5-f-i), which built the boundary this file makes the rest of the planner
// respect.
//
// # The collision
//
// goopg has an older answer to the same question the boundary map answers.
// `buildBindingsPosMap` / `applyJoinTreePosMap` (bushy.go) exist because the
// integer DP and the MHJ packer REORDER a join tree in place and then leave
// every expression above and inside it addressing the pre-rewrite column
// positions. The repair walks the finished tree, collects its scan leaves in
// DFS order, derives "FROM-clause offset → actual plan offset", and rewrites
// ColumnRef indices through it. `reconcileNLILayout` is the third member: a
// final bottom-up pass that re-resolves join keys and node schemas by NAME.
//
// The PG-shaped search answers the same question in the opposite direction. Its
// arms never leave a stale coordinate behind — each `createPlan` arm translates
// its own clauses onto its own merged row as it builds it (P5.5-e-i), and the
// boundary republishes the root in pre-search binding order (P5.5-f-i). By the
// time a searched subtree is spliced into the enclosing tree there is nothing
// left to correct.
//
// # What was already covered, and what was not
//
// Half of this was already true and it is worth saying which half, because the
// half that was true is load-bearing and undocumented.
//
// When the boundary emitted its `Project`, the legacy family ALREADY stopped
// there — not for any reason to do with the search, but because M0125-0012 made
// EVERY `*Project` in a join tree an opaque scope boundary on both sides of the
// map: `collect` advances past one without descending, and
// `applyJoinTreePosMap` returns at one without descending. A measured probe
// confirms it: over a boundary Project, `buildBindingsPosMap` returns nil and
// no target index moves. That rule exists for FROM-subqueries and inherited the
// search's case for free.
//
// The hole is the ELIDED root. When the search's order already IS binding order
// the boundary emits no Project (P5.5-f-i), and the root handed to the
// enclosing tree is a bare `*Join` — which both passes walk straight into. The
// numeric half of that is provably harmless (an identity layout yields an
// identity map, because `collect`'s DFS order over a join is its output order).
// The other half is not numeric at all: `applyJoinTreePosMap`'s `*Join` arm
// calls `reresolveJoinByName`, and `reconcileNLILayout` is name resolution end
// to end. Those rebind the searched joins' keys by (Name, SourceTableIdx)
// against child schemas — over quals the `createPlan` arms had just bound by
// coordinate, one node earlier, from the layout.
//
// Two things follow, and they are the two halves of this file:
//
//  1. the searched subtree must be recognisable at its root whether or not a
//     Project was needed, so the opacity is a property of the SEARCH rather
//     than a side effect of a Project rule that could be revised for
//     FROM-subquery reasons tomorrow; and
//  2. "name resolution would have agreed anyway" must be CHECKED rather than
//     assumed, because it is the only remaining evidence that the arms'
//     coordinate arithmetic is right — see
//     `assertSearchedTreeNeedsNoReconcile`.
//
// # Why the tag is a field and not a node
//
// The alternative was a wrapper node. P5.5-f-i already pays for one extra node
// at the boundary and only when the search actually reordered; a second,
// unconditional one would put a per-row copy in the path of every plan the
// search touches, to carry one bit. A bit is what is carried, so a bit is what
// is stored: `searchedTree` is an empty-in-the-common-case embedded struct on
// exactly the node kinds `createPlanAtSearchRoot` can return as a root.
//
// `markSearchedTree` PANICS on any other kind rather than silently declining.
// A future `createPlan` arm that returns a new root kind must be taught about
// the tag, and the failure mode of forgetting is a silently untagged subtree —
// i.e. the double-permutation above, which produces a plan that runs and
// returns wrong rows. That is the class this whole milestone exists to remove
// from the planner, so it fails loudly here.
//
// # Live since M0127-P5.9 (2026-08-06)
//
// `GOOPG_PGSHAPED_DP` defaults ON and `planSelect` calls the search, so
// production trees DO carry the tag and all three skips are reachable on real
// plans — the same correction enclosingtree.go's header already carries.

import "fmt"

// searchedTree is the tag. It is embedded (not a named field) so that every
// carrier gets the same two methods, and so that `isSearchedTree` can ask for
// them through one interface instead of a type switch that must be kept in sync
// with the carrier list.
type searchedTree struct {
	fromJoinSearch bool
}

func (t *searchedTree) markFromJoinSearch()    { t.fromJoinSearch = true }
func (t *searchedTree) isFromJoinSearch() bool { return t.fromJoinSearch }

// searchRootNode is the carrier interface. Embedding `searchedTree` in a node
// type is the whole of implementing it.
type searchRootNode interface {
	Node
	markFromJoinSearch()
	isFromJoinSearch() bool
}

// markSearchedTree tags n as the root of a subtree the PG-shaped join search
// produced, and returns it for chaining.
//
// The panic is deliberate — see the file header. The carrier set is the set of
// node kinds `createPlanNode` can return at the root: the join arms' `*Join` and
// `*NestedLoopIndexJoin`, the scan arms' `*SeqScan` / `*IndexScan` and the
// `*Filter` their leaf rewrapper can restore around one, `*Sort` from the
// pathkey arm, and `*Project` from the boundary itself.
func markSearchedTree(n Node) Node {
	if n == nil {
		panic("createPlan: asked to tag a nil search root")
	}
	s, ok := n.(searchRootNode)
	if !ok {
		panic(fmt.Sprintf("createPlan: %T cannot carry the searched-subtree tag; a createPlan arm returns a root kind that the legacy posmap family would still walk into (embed searchedTree in it)", n))
	}
	s.markFromJoinSearch()
	return n
}

// isSearchedTree reports whether n is the root of a searched subtree. Nodes
// that cannot carry the tag answer false, which is the correct answer for every
// tree the search did not build.
func isSearchedTree(n Node) bool {
	s, ok := n.(searchRootNode)
	return ok && s.isFromJoinSearch()
}

// searchedTreeWidth is the number of columns a searched subtree publishes. It
// exists so `buildBindingsPosMap`'s collector has one named, documented reason
// to advance its running offset past the subtree without descending into it —
// the same treatment it already gives `*CTEScan`, `*Values` and the FROM-clause
// set-returning functions, and for the same reason: an opaque leaf whose own
// columns need no remap but whose WIDTH every scan to its right depends on.
//
// The searched subtree is stronger than those, though. They are opaque because
// their internals are a different planning scope; this one is opaque because
// its columns are already at the positions the bindings put them, so the map
// the collector would build for them is the identity by construction. Recording
// no scan entry for them is exactly right: `buildBindingsPosMap`'s returned
// closure leaves a binding it has no entry for at its original index.
func searchedTreeWidth(n Node) int { return len(n.Output()) }

// assertSearchedTreeNeedsNoReconcile is P5.5-f-ii's no-op assertion: it runs
// the real `reconcileNLILayout` over a searched subtree and panics if the pass
// MOVED any ColumnRef.
//
// The point is that it is an INDEPENDENT check. `reconcileNLILayout` resolves
// by (Name, SourceTableIdx) against the child schemas; the `createPlan` arms
// bound the same references by coordinate arithmetic over `outputLayout`. Two
// unrelated mechanisms arriving at the same index is evidence the arms'
// arithmetic is right; a disagreement is a bug in one of them, and either way
// it is worth knowing at the boundary rather than in a row count.
//
// # Why it runs below the boundary Project and not on it
//
// The caller applies this to the join tree the arms built, before the boundary
// `Project` goes on top. That is a scoping choice, not a claim that the pass
// would damage the Project: measured, it does not — the targets carry the Name
// of the column they mean, so resolving them by name against the child returns
// the index they already hold. It is scoped below because the Project's target
// list is not a REFERENCE, it is the coordinate map itself, and an assertion
// about references should not be asked about it. What the boundary node relies
// on is the skip, and the skip is unconditional.
//
// # Where it abstains, which is the part to remember
//
// Name resolution is a check that agrees where it can see and says nothing
// where it cannot. It is not a second implementation of 03 §10, and three
// specific blind spots follow from that:
//
//   - an UNNAMED operand. `reresolveJoinByName` returns immediately on a
//     `ColumnRef` with an empty Name, so a clause built from bare positional
//     refs is invisible to the assertion. Production clauses come from the
//     resolved WHERE predicate and carry names; the P5.5-e unit fixtures did
//     not, which is why `searchedtree_test.go` supplies its own named clause
//     helper rather than reusing `equiClauseOn` — reused, every assertion
//     about this function would have passed for the wrong reason.
//   - an AMBIGUOUS name — two columns with the same name and no distinguishing
//     `SourceTableIdx`. Resolution returns -1 and the index is left alone, so a
//     permutation among identically-named columns passes.
//   - anything the pass does not touch at all: it rebinds join keys, join
//     predicates and node schemas, not every expression in the tree.
//
// What it does catch is a swapped or off-by-one coordinate on a named join key
// — which is the mistake the arms' layout arithmetic can plausibly make, and
// the one that produces a plan that runs and joins on the wrong column. The
// structural checks in `boundaryMap` (hole, out-of-range, duplicate) are not
// replaceable by this and remain the primary guard.
//
// The assertion mutates: it lets the real pass run and then compares. That is
// safe precisely because the claim is that it changes nothing; if it changed
// something the tree was already wrong and the process is about to stop.
func assertSearchedTreeNeedsNoReconcile(n Node) {
	refs, before := snapshotColumnRefIndices(n)
	reconcileNLILayoutBody(n)
	for i, cr := range refs {
		if cr.Index != before[i] {
			panic(fmt.Sprintf("createPlan: searched subtree needed reconciliation — name resolution moves %q from column %d to %d; the createPlan arm that bound it and reconcileNLILayout disagree about the layout",
				cr.Name, before[i], cr.Index))
		}
	}
}

// snapshotColumnRefIndices records every same-scope `*ColumnRef` reachable from
// n together with the index it currently holds. The two slices are parallel and
// the pointers are into the live tree, so a later re-read of `refs[i].Index`
// observes whatever a pass in between did to it.
//
// It walks with `walkPlanExprs`, which is broader than the set
// `reconcileNLILayout` rewrites (it descends into sublink operands, which the
// reconcile pass leaves alone). Broader is the right direction for an
// assertion: an extra reference the pass does not touch cannot move, so it
// costs nothing and it catches a pass that reaches somewhere it should not.
func snapshotColumnRefIndices(n Node) ([]*ColumnRef, []int) {
	var refs []*ColumnRef
	var idx []int
	walkPlanExprs(n, func(e Expr) {
		if cr, ok := e.(*ColumnRef); ok {
			refs = append(refs, cr)
			idx = append(idx, cr.Index)
		}
	})
	return refs, idx
}
