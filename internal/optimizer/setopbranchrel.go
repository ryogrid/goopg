package optimizer

// setOpBranchTag carries a SETOP upper rel out of `createSetOpPaths` on the
// node that call returns, the way `searchedTree` carries the join search's
// upper rel out of `planJoinlistSearch` (searchedtree.go).
//
// WHY IT EXISTS (M0144-0003b-1). PG has no chain: `pull_up_simple_union_all`
// (prepjointree.c:1617) flattens `a UNION ALL b UNION ALL c` into ONE
// appendrel with three children, so `add_paths_to_append_rel`
// (allpaths.c:1321) sees every branch at once and one Parallel Append covers
// the whole union. goopg builds a LEFT-DEEP `*SetOp` chain instead — one
// two-child node per `UNION ALL` keyword — and prices each link on its own
// SETOP rel (`createSetOpPaths`).
//
// That difference is inert for the serial plan (a left-deep chain of
// streaming set-ops emits the same rows in the same order as an N-way
// Append) but it used to kill both parallel arms at the SECOND link:
// `searchedRelOf` stops at any node with more or fewer than one boundary
// child (searchedtree.go:184), and a `*SetOp` has two — so the outer link's
// left branch reported no rel at all, `ConsiderParallel` went false, and
// `addPartialSetOpPath` returned before either arm could look at it.
// Measured on the TPC-DS SF0.25 corpus: the INNERMOST link of every chain
// admitted fine (Q14/Q71/Q76 each filed a partial path, two of them won a
// Gather over it), and every OUTER link reported `nil-rel` on the side that
// held the rest of the chain. The same hole swallowed the `*Gather`
// `createSetOpPaths` returns when the inner link's partial path wins: it is
// a plain wrapper to `searchedRelOf`, which then descends past it into the
// `*SetOp` underneath and stops there too.
//
// Carrying the rel on the returned node closes both cases with the mechanism
// the file already uses for the search's own rel, and composes: link 2 reads
// link 1's rel, link 3 reads link 2's, so the chain's partial paths stack
// into exactly the plan PG reaches by flattening. The tag is set ONLY by
// `createSetOpPaths`, so a `*Gather` or `*SetOp` built anywhere else reads
// nil and the old behaviour stands.
type setOpBranchTag struct {
	setOpRel *RelOptInfo
}

func (t *setOpBranchTag) setSetOpBranchRel(rel *RelOptInfo) { t.setOpRel = rel }
func (t *setOpBranchTag) setOpBranchRel() *RelOptInfo       { return t.setOpRel }

// setOpBranchRelNode is the carrier interface. Embedding `setOpBranchTag` in
// a node type is the whole of implementing it.
type setOpBranchRelNode interface {
	Node
	setSetOpBranchRel(*RelOptInfo)
	setOpBranchRel() *RelOptInfo
}

// stampSetOpBranchRel attaches `rel` to `n` when `n` can carry it. Returns n
// unchanged otherwise — a set operation whose winning path built some other
// node kind simply stays opaque to the link above, as it was before.
func stampSetOpBranchRel(n Node, rel *RelOptInfo) Node {
	if c, ok := n.(setOpBranchRelNode); ok && rel != nil {
		c.setSetOpBranchRel(rel)
	}
	return n
}

// setOpBranchRelOf is `searchedRelOf` widened by one terminus: a node
// carrying a SETOP rel answers with that rel.
//
// It is the accessor `createSetOpPaths` uses for its two branches, and ONLY
// that — every other consumer of a branch's rel wants the join search's rel
// specifically and keeps calling `searchedRelOf`. The walk is otherwise
// identical, including the depth cap, so a branch that is a searched tree
// under boundary wrappers answers exactly as it did before.
//
// Carrier check comes FIRST: a `*Gather` over a partial `*SetOp` is both a
// carrier and a single-child boundary node, and descending past it would
// land on the `*SetOp` — which is itself a carrier, so the answer would be
// the same rel, but only by luck of this particular shape. Answering at the
// outermost carrier is the rule that holds for every shape.
func setOpBranchRelOf(n Node) *RelOptInfo {
	for depth := 0; n != nil && depth < 32; depth++ {
		if c, ok := n.(setOpBranchRelNode); ok {
			if rel := c.setOpBranchRel(); rel != nil {
				return rel
			}
		}
		if s, ok := n.(searchRootNode); ok && s.isFromJoinSearch() {
			return s.searchedRel()
		}
		kids := boundaryWalkChildren(n)
		if len(kids) != 1 {
			return nil
		}
		n = kids[0]
	}
	return nil
}
