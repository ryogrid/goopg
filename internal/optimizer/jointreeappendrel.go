package optimizer

import "github.com/goopg/goopg/internal/parser"

// M0145-0004 — UNION ALL subqueries as appendrel leaves (design doc
// docs/design/0100-0149/m0145-0004-union-all-appendrel-leaf.md).
//
// PG's pull_up_simple_union_all (prepjointree.c:1617) flattens a
// FROM-clause `(... UNION ALL ...) alias` into an appendrel: one leaf in
// the parent jointree whose pathlist add_paths_to_append_rel files from
// the member rels' pathlists — including the Parallel Append TPC-DS Q5's
// `Parallel Hash Join → Parallel Append` is built from. goopg does not
// give members parent-level rtable entries — the members' plans are
// finished Nodes inside the leaf's nested scope — so what this slice
// lifts across the boundary is the candidate PG's appendrel exists to
// produce: the partial UNION ALL path, re-targeted from the nested
// scope's SETOP rel (which createSetOpPaths already populated and
// M0144-0003b-1's setOpBranchTag already carries out on the leaf node)
// onto the parent leaf rel.
//
// Two gates keep it fail-closed to the exact legacy leaf:
//
//   - mark: `rangeBinding.appendrel`, set only when the jointree
//     pipeline is armed AND the subquery passes the
//     is_simple_union_all port below AND the item is not LATERAL —
//     the legacy arm never sets it;
//   - hoist: `addAppendRelPartialPaths` files the re-targeted paths
//     only for a leaf whose ROOT is the SETOP-rel carrier (a Filter /
//     Sort / Limit / Project wrapper would drop rows the hoisted
//     emission cannot reproduce) and only through addPartialPath's
//     own ParallelSafe/ConsiderParallel gate.

// subqueryChainIsSimpleUnionAll is `is_simple_union_all`
// (prepjointree.c:2214) over goopg's right-leaning SetOp chain: every
// link must be `UNION ALL`, and the UNION's own ORDER BY / LIMIT /
// OFFSET / row marks / WITH refuse. The parser's trailer lift
// (foldSetOps) already moved an unparenthesised trailing member's
// ORDER BY/LIMIT onto the chain head, so the top-level check covers
// `A UNION ALL B ORDER BY 1`; a parenthesised member keeps its own
// clauses on its own SelectStmt, where they belong to the member and —
// as in PG, whose leaf subqueries keep their own sortClause — do not
// block the union's flattening.
//
// The tlist_same_datatypes half of PG's recursion is NOT ported:
// goopg's parser AST carries no resolved types at the point this runs
// and the produced node's member schemas are post-cast. See the design
// doc's deferral list.
func subqueryChainIsSimpleUnionAll(s *parser.SelectStmt) bool {
	if s == nil || s.SetOp == nil {
		return false
	}
	if len(s.OrderBy) > 0 || s.Limit != nil || s.Offset != nil ||
		len(s.Locking) > 0 || s.With != nil {
		return false
	}
	return unionAllChainLinks(s)
}

// unionAllChainLinks walks the right-leaning SetOp chain head-down:
// every link must be UNION ALL, and a member that is itself a compound
// must be UNION ALL all the way down. A `SetOpOperand` grouping node
// (post-paren trailing text, M0125-0020) declines: its own slots carry
// syntax the flat chain walk cannot attribute, and the case does not
// occur in the parity corpus.
func unionAllChainLinks(s *parser.SelectStmt) bool {
	if s.SetOpOperand != nil {
		return false
	}
	for cur := s; cur.SetOp != nil; {
		if cur.SetOp.Type != parser.SetOpUnion || !cur.SetOp.All {
			return false
		}
		// The left member of each link IS `cur` itself (SetOpClause
		// carries only Right): its compound-ness is `cur.SetOpOperand`,
		// refused at the head above and at the previous iteration's
		// right check — is_simple_union_all_recurse's larg side.
		right := cur.SetOp.Right
		if right == nil || right.SetOpOperand != nil {
			return false
		}
		// Trailing FOR UPDATE binds to the LAST member's Locking in
		// this AST — `A UNION ALL B FOR UPDATE` is union-level syntax
		// landing member-locally. PG's rowMarks check covers the same
		// net refusal (and a member LockRows would fail the parallel
		// gates downstream regardless); refuse at mark time.
		if len(right.Locking) > 0 {
			return false
		}
		if right.Parenthesized && right.SetOp != nil {
			// A parenthesised compound member: the planner treats it
			// atomically (the fold stops cutting there), so its own
			// chain must independently be all UNION ALL — PG's
			// is_simple_union_all_recurse on the nested
			// SetOperationStmt.
			if !unionAllChainLinks(right) {
				return false
			}
			return true
		}
		cur = right
	}
	return true
}

// appendRelPartialProducer is the DPPATH trace producer label for the
// hoisted appendrel leaf partial path.
const appendRelPartialProducer = "baserel.appendrel.partial"

// addAppendRelPartialPaths files the appendrel leaf's partial path —
// add_paths_to_append_rel's partial arm (allpaths.c:1538-1627) at the
// granularity goopg's seam can express: the nested scope's
// addPartialSetOpPath already computed the Parallel Append candidate on
// its SETOP rel (partial member picks, the mixed arm's claimed-whole
// picks, PG's worker-count and cost rules); this lifts each filed path
// onto the leaf rel so the parent's join search can place it.
//
// Runs in the `create_plain_partial_paths` slot — after
// set_rel_consider_parallel and addBaseRelPartialPaths, before
// create_index_paths — the position PG's base-rel path generation gives
// the append path.
func (s *searchCtx) addAppendRelPartialPaths() {
	if s == nil || !s.parallelModeOK || len(s.joinrels) < 2 {
		return
	}
	for i, rel := range s.joinrels[1] {
		if i >= len(s.relInfos) || !s.relInfos[i].appendrel {
			continue
		}
		if !rel.ConsiderParallel {
			continue
		}
		// The carrier must sit AT the leaf root: any wrapper —
		// leaf-local Filter, Sort, Limit, Project, LockRows —
		// changes or decorates rows the hoisted partial emission
		// cannot reproduce. A bare *SetOp or the union's own
		// *Gather/*GatherMerge top is admitted: the hoisted path
		// emits the partial SetOp BELOW the gather, not the gather.
		carrier, ok := rel.baseLeaf.(setOpBranchRelNode)
		if !ok {
			continue
		}
		// tlist_same_datatypes, the half of is_simple_union_all_recurse
		// (prepjointree.c:2258) that subqueryChainIsSimpleUnionAll cannot
		// answer: at MARK time the parser AST carries no resolved types,
		// and by the time a schema exists setOpUnifyBranches has already
		// coerced every branch, so the comparison there is a tautology.
		// The verdict is therefore taken where the pre-cast types live —
		// stamped on the node as SetOp.TlistTypesDiffer (planner.go) — and
		// consulted here, the last gate before the leaf inherits an
		// appendrel's partial path. Upstream refuses the flattening
		// outright; refusing the hoist is that refusal expressed at the
		// granularity this seam has, and it leaves the exact legacy leaf.
		// M0145-0004.
		if so := carrierSetOpNode(rel.baseLeaf); so != nil && so.TlistTypesDiffer {
			continue
		}
		setOpRel := carrier.setOpBranchRel()
		if setOpRel == nil || len(setOpRel.PartialPathlist) == 0 {
			continue
		}
		for _, pp := range setOpRel.PartialPathlist {
			if pp == nil {
				continue
			}
			// The path is the same Parallel Append candidate; only
			// its owner relid moves — from the nested scope's SETOP
			// rel to this leaf rel. Children, Cost, Rows,
			// ParallelWorkers/Aware, DisabledNodes and the
			// SetOpLeft/RightNonPartial marks all carry unchanged:
			// they describe the emission, which is identical.
			hoisted := *pp
			hoisted.Rel = rel
			addPartialPath(rel, &hoisted, appendRelPartialProducer)
		}
	}
}

// carrierSetOpNode finds the `*SetOp` under an admitted appendrel carrier.
//
// The carrier is a bare `*SetOp` or the union's own `*Gather`/`*GatherMerge`
// top (see the root check above), so the node the type verdict is stamped on
// is not always the carrier itself. The walk descends single-child boundary
// nodes with the same depth cap `setOpBranchRelOf` uses, and answers nil for
// any shape that is not one of those — which leaves the gate open, i.e. the
// pre-M0145-0004-tlist behaviour, rather than refusing a shape it cannot
// read.
func carrierSetOpNode(n Node) *SetOp {
	for depth := 0; n != nil && depth < 32; depth++ {
		if so, ok := n.(*SetOp); ok {
			return so
		}
		kids := boundaryWalkChildren(n)
		if len(kids) != 1 {
			return nil
		}
		n = kids[0]
	}
	return nil
}
