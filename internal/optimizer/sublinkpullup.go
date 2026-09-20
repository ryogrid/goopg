package optimizer

// Sublink pull-up admissibility — M0142-0008a-3i-route-a step 1.
//
// This file answers ONE question about an unplanned sublink body: may it be
// flattened into the enclosing query's join list, or must it stay a plan of
// its own? It is goopg's port of `is_simple_subquery`
// (postgres/src/backend/optimizer/prep/prepjointree.c:1807), the gate
// `pull_up_subqueries` (:1083) consults before pulling a subquery RTE into
// the parent's range table.
//
// Nothing calls it yet, by design. Step 1 of the route task is the
// prerequisite pair — retain the parse tree (ExistsExpr.Subquery) and be able
// to classify it — and both halves must be provably inert before the splice
// that consumes them is attempted. The repository has landed groundwork under
// exactly this posture before: `pathkeysCountContainedIn` (M0141-S7) and
// `costIncrementalSort` (M0141-S7b) both shipped with zero production callers
// and a unit test pinning the formula against upstream.

import "github.com/goopg/goopg/internal/parser"

// sublinkBodyIsSimple reports whether an unplanned sublink body can be pulled
// up into the enclosing join list.
//
// Every refusal below is one of upstream's, in upstream's order. The
// upstream comment explains the shape of the rule and it holds here verbatim:
// a body that groups, aggregates, sorts, limits, de-duplicates or locks
// computes something the enclosing query's join search cannot reproduce by
// merely joining the body's relations, so its result is not substitutable for
// its inputs.
//
//	if (subquery->setOperations) return false;
//	if (subquery->hasAggs || subquery->hasWindowFuncs ||
//	    subquery->hasTargetSRFs || subquery->groupClause ||
//	    subquery->groupingSets || subquery->havingQual ||
//	    subquery->sortClause || subquery->distinctClause ||
//	    subquery->limitOffset || subquery->limitCount ||
//	    subquery->hasForUpdate || subquery->cteList) return false;
//
// Two upstream arms are NOT ported, and their absence is safe here rather
// than merely unimplemented:
//
//   - `rte->security_barrier`. That flag lives on the range-table entry PG
//     built for a subquery-in-FROM. A sublink body reaching this predicate
//     came from `EXISTS (...)` / `IN (...)` in a qual, where upstream's own
//     `convert_EXISTS_sublink_to_join` (subselect.c:1450) synthesises the RTE
//     rather than inheriting a view's — so the flag is false by construction
//     in the case this predicate serves. A body that reads a security-barrier
//     view still has that view's own RTE INSIDE it, which this pull-up never
//     touches.
//   - the `rte->lateral` block. Same reasoning: the RTE is synthesised and
//     not marked LATERAL. goopg's correlation references are
//     `OuterColumnRef{Level:1}` in the PLANNED body, a representation this
//     predicate never sees — it reads the parse tree. Re-basing those
//     references is step 2's obligation and is recorded as such on the task;
//     it is deliberately not disguised as an admissibility question.
//
// The VALUES case is refused rather than accepted: `VALUES` bodies carry no
// FROM items to splice, so "simple" would be true but vacuous, and the caller
// that does not yet exist would have nothing to do with the answer.
func sublinkBodyIsSimple(s *parser.SelectStmt) bool {
	if s == nil {
		return false
	}
	// A grouping node stands for `( operand )` and holds no clauses of its
	// own; its value is the operand's. Refuse rather than descend — the
	// trailing ORDER BY/LIMIT it may carry belongs to the node ABOVE the
	// parenthesised query (see parser.SelectStmt.SetOpOperand), so the
	// answer for the pair is not the answer for the operand.
	if s.SetOpOperand != nil {
		return false
	}
	// `subquery->setOperations`.
	if s.SetOp != nil {
		return false
	}
	// `subquery->cteList`.
	if s.With != nil {
		return false
	}
	// `subquery->groupClause` / `groupingSets` / `havingQual`.
	if len(s.GroupBy) > 0 || s.Having != nil {
		return false
	}
	// `subquery->sortClause`.
	if len(s.OrderBy) > 0 {
		return false
	}
	// `subquery->distinctClause` — goopg splits plain DISTINCT from
	// DISTINCT ON across two fields; upstream has one clause for both.
	if s.Distinct || len(s.DistinctOn) > 0 {
		return false
	}
	// `subquery->limitCount` / `limitOffset`. WithTies cannot be set without
	// a Limit, but it is checked anyway so the arm does not silently depend
	// on that invariant holding.
	if s.Limit != nil || s.Offset != nil || s.WithTies {
		return false
	}
	// `subquery->hasForUpdate`. Upstream's comment: pulling up would make
	// the locking occur semantically higher than it should.
	if len(s.Locking) > 0 {
		return false
	}
	// A VALUES body has no FROM items to splice — see the header.
	if len(s.ValuesRows) > 0 {
		return false
	}
	// A body with no relation at all (`SELECT 1` with no FROM) likewise
	// offers the join search nothing.
	if len(s.From) == 0 && len(s.FromExprs) == 0 {
		return false
	}
	// `subquery->hasAggs` / `hasWindowFuncs` / `hasTargetSRFs`. goopg's
	// parser sets no such summary flags, so they are derived here from the
	// only two places a sublink body can carry one: its target list and its
	// WHERE. (GROUP BY/HAVING/ORDER BY are already refused above, so an
	// aggregate hiding in one of those cannot reach this point.)
	if selectListOrQualHasAggOrWindow(s) {
		return false
	}
	return true
}

// selectListOrQualHasAggOrWindow reports whether the body's target list or
// WHERE clause contains an aggregate call, a window call, or a WITHIN GROUP /
// FILTER decoration — the parse-tree evidence for upstream's `hasAggs` and
// `hasWindowFuncs` summary flags.
//
// A set-returning function in the target list is upstream's `hasTargetSRFs`.
// goopg has no catalog-free way to know whether an arbitrary function is
// set-returning at this point (the check would need the catalog the resolver
// has and this predicate does not), so that arm is NOT claimed here: a body
// whose target list calls an SRF is currently accepted. Recorded on the task
// and in the ledger as the one upstream arm this port leaves open, with the
// consequence stated — step 2 must either take a catalog argument or refuse
// any target-list FuncCall it cannot classify.
func selectListOrQualHasAggOrWindow(s *parser.SelectStmt) bool {
	found := false
	note := func(fc *parser.FuncCall) error {
		if fc == nil {
			return nil
		}
		if fc.Over != nil || len(fc.WithinGroup) > 0 || fc.Filter != nil {
			found = true
			return nil
		}
		if isAggregateFuncName(fc) {
			found = true
		}
		return nil
	}
	for _, t := range s.Targets {
		_ = walkExpr(t.Expr, note)
		if found {
			return true
		}
	}
	_ = walkExpr(s.Where, note)
	return found
}
