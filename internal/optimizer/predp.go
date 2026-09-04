package optimizer

// S5a (design bundle correlated-subquery-planning, D3.1): run sublink
// pull-up BEFORE join-order search.
//
// Historically the pipeline ran the join-order search + predicate pushdown first
// and unnested sublinks afterwards, so decorrelated semi/anti joins
// wrapped an already-searched join tree and their sunk residual
// conjuncts never participated in join search. Upstream runs
// pull_up_sublinks before join planning (subquery_planner →
// pull_up_sublinks, postgres/src/backend/optimizer/prep/prepjointree.c);
// this file moves goopg the same way while keeping the decorrelated
// semi/anti joins PINNED at their post-unnest positions — DP
// participation for semi/anti (S5b) is deferred by user decision
// (2026-07-21), so DP runs only on the subtree BELOW the pinned spine.
//
// Engagement is deliberately narrowed (documented scope for S5a): the
// pre-DP position engages only when the WHERE predicate's sublinks are
// exclusively of the EXISTS/IN family. Scalar sublinks
// (SubqueryExpr-family) produce INNER-join / aggregate-above-join
// rewrites whose spine nodes (Aggregate, OrdinalityWrap) would need
// their own post-DP re-resolution machinery; those statements keep the
// legacy order (DP first, unnest after), which is exactly today's
// behavior for them. Widening the engaged family is future work and
// nothing here precludes it.

import "github.com/goopg/goopg/internal/catalog"

// whereEligibleForPreDPUnnest reports whether the resolved WHERE
// predicate contains only EXISTS/IN-family sublinks (or none). Any
// scalar-family sublink (SubqueryExpr, ArraySubqueryExpr,
// MultiAssignSubqRow) anywhere in the expression tree — including
// hidden in an IN operand — sends the statement down the legacy
// pipeline order.
func whereEligibleForPreDPUnnest(pred Expr) bool {
	eligible := true
	walkExprTree(pred, func(e Expr) {
		switch e.(type) {
		case *SubqueryExpr, *ArraySubqueryExpr, *MultiAssignSubqRow:
			eligible = false
		}
	})
	return eligible
}

// runJoinSearchBelowPinned runs the join-order search (tryJoinSearch +
// pushPredicatesIntoCrossJoins — verbatim the historical DP block) on
// the subtree BELOW the pinned spine that pre-DP unnesting produced.
//
// Expected tree shapes on entry (root):
//
//	Filter{pred}(origChain)                          — unnest declined
//	[Filter{retained}](Semi/Anti…(Filter{sunk}(origChain)))
//	[Filter{retained}](Semi/Anti…(origChain))        — nothing sunk
//
// The helper descends through retained Filters and pinned semi/anti
// joins to the node owning origChain, runs the DP block there, splices
// the result back, and — when join search actually changed the outer
// layout — re-resolves the spine bottom-up:
//
//   - each pinned semi/anti join's keys/predicate via
//     reresolveJoinByName (the same machinery rewriteMultiWayChain
//     uses when MHJ re-sorts the layout under a pinned join), plus an
//     outer-only schema refresh (semi/anti publish Left's schema; the
//     reresolve helper deliberately never widens it);
//   - retained Filter predicates via the old→new layout position map;
//   - retained sublinks' inner-plan OuterColumnRefs via
//     remapOuterRefsInSubplan, mirroring the MHJ path.
//
// Degenerate case (unnest declined → root IS Filter{pred}(origChain)):
// the DP block runs on exactly the input it would have received at the
// legacy position — byte-identical plans for sublink-free statements.
func runJoinSearchBelowPinned(root Node, origChain Node, ctx *resolveContext, cat catalog.Catalog) Node {
	newRoot := root
	setResult := func(n Node) { newRoot = n }

	var spineJoins []*Join
	var spineFilters []*Filter

	put := setResult
	cur := root
	var target Node
descend:
	for {
		switch x := cur.(type) {
		case *Filter:
			if x.Child == origChain {
				target = x
				break descend
			}
			spineFilters = append(spineFilters, x)
			f := x
			put = func(n Node) { f.Child = n }
			cur = x.Child
		case *Join:
			if x.Type != JoinTypeSemi && x.Type != JoinTypeAnti {
				// Not a pinned spine node — should not happen under
				// the eligibility pre-check. Leave the tree as built
				// (correct, unoptimised join order).
				return newRoot
			}
			spineJoins = append(spineJoins, x)
			j := x
			put = func(n Node) { j.Left = n }
			cur = x.Left
		default:
			if cur == origChain {
				// Bare chain below the spine: no residual Filter means
				// no join-search predicate — the historical block only
				// runs DP under a Filter, so there is nothing to do.
				return newRoot
			}
			return newRoot
		}
	}

	f, ok := target.(*Filter)
	if !ok {
		return newRoot
	}

	// Layout snapshot before join search, for staleness detection.
	oldSchema := append(Schema(nil), f.Child.Output()...)

	// The historical DP block, verbatim (planner.go legacy position).
	//
	// Take2 P4-01 Slice 3: when a pinned spine sits above the searched
	// subtree, its quals and retained filters read that subtree's output
	// from above — references the per-joinrel derivation cannot see — so
	// parent-aware narrowing is declined for this search (the Slice-2
	// arms still run). The flag is save/restored: the context outlives
	// this call.
	var newTarget Node = f
	if ctx != nil && len(spineJoins)+len(spineFilters) > 0 {
		saved := ctx.pinAbove
		ctx.pinAbove = true
		defer func() { ctx.pinAbove = saved }()
	}
	if newChild, newPred := tryJoinSearch(f.Child, f.Predicate, ctx, cat); newPred == nil {
		newTarget = newChild // all conjuncts consumed → remove Filter
	} else if newChild != f.Child {
		f.Child = newChild
		f.Predicate = newPred
		newTarget = pushPredicatesIntoCrossJoins(f)
	} else {
		newTarget = pushPredicatesIntoCrossJoins(f)
	}
	put(newTarget)

	// Re-resolve the pinned spine only when join search changed the
	// outer layout. On a shape the search declines tryJoinSearch is a no-op and
	// pushdown never reorders, so this is the common fast path.
	newSchema := newTarget.Output()

	// M0127-P5.5-f-ii-b: the PG-shaped search's boundary, consumed.
	//
	// 03 §10 says this re-resolution "survives and consumes the boundary
	// map". When the spliced subtree came out of the PG-shaped search,
	// consuming that map means doing NOTHING — the boundary republishes
	// the root in pre-search binding order (P5.5-f-i), so the map is the
	// identity and every rebind below would be a no-op at best.
	//
	// Skipping it is not the interesting part; PROVING the skip is. The
	// identity is asserted from the schemas rather than inferred from
	// `layoutPosMap` returning nil, because nil is also how that helper
	// says "widths differ, refuse to remap" — see
	// assertSpineConsumesIdentityBoundaryMap. The tripwire then runs over
	// the whole enclosing tree, which is the reference-side half of the
	// same claim (03 §10's build-mode assertion, widened from the boundary
	// node in P5.5-f-i to the tree it guards).
	//
	// Live since M0127-P5.9 (2026-08-06): `GOOPG_PGSHAPED_DP` is ON by
	// default, so splicedSearchedRoot DOES find tagged roots in production
	// and both assertions below run on real plans.
	if splicedSearchedRoot(newTarget) != nil {
		assertSpineConsumesIdentityBoundaryMap(oldSchema, newSchema)
		assertEnclosingTreeColumnRefs("pinned-spine enclosing tree", newRoot)
		return newRoot
	}

	pm := layoutPosMap(oldSchema, newSchema)
	if pm == nil {
		return newRoot
	}
	// Bottom-up: the join closest to the spliced subtree first, so
	// each parent rebinds against an already-refreshed child schema.
	for i := len(spineJoins) - 1; i >= 0; i-- {
		j := spineJoins[i]
		reresolveJoinByName(j)
		// Semi/anti publish the OUTER schema only; reresolveJoinByName
		// deliberately never widens it, but after the splice the
		// cached copy still reflects the pre-DP left layout — refresh
		// it to the new Left output (still outer-only).
		j.schema = append(Schema(nil), j.Left.Output()...)
	}
	for i := len(spineFilters) - 1; i >= 0; i-- {
		fl := spineFilters[i]
		remapByPosMap(&fl.Predicate, pm)
		remapSublinkOuterRefs(fl.Predicate, pm)
	}
	return newRoot
}

// layoutPosMap builds an old-index → new-index map between two layouts
// of the same column set, matching by (Name, SourceTableIdx). Returns
// nil when the layouts are identical (nothing to remap). Columns that
// cannot be matched uniquely map to themselves — with both schemas
// produced from the same scan set this only happens for genuinely
// ambiguous unnamed columns, where keeping the old index is the least
// wrong choice.
func layoutPosMap(oldSchema, newSchema Schema) func(int) int {
	if len(oldSchema) != len(newSchema) {
		// Widths differ (should not happen: join search preserves the
		// column multiset) — refuse to remap rather than corrupt.
		return nil
	}
	identical := true
	for i := range oldSchema {
		if oldSchema[i].Name != newSchema[i].Name ||
			oldSchema[i].SourceTableIdx != newSchema[i].SourceTableIdx {
			identical = false
			break
		}
	}
	if identical {
		return nil
	}
	m := make([]int, len(oldSchema))
	for i := range oldSchema {
		m[i] = i
		if idx := findColumnIndexByNameAndSource(newSchema, oldSchema[i].Name, oldSchema[i].SourceTableIdx, 0); idx >= 0 {
			m[i] = idx
		} else if idx := findUniqueColumnIndex(newSchema, oldSchema[i].Name, 0); idx >= 0 {
			m[i] = idx
		}
	}
	return func(i int) int {
		if i >= 0 && i < len(m) {
			return m[i]
		}
		return i
	}
}

// remapSublinkOuterRefs applies the layout position map to the
// OuterColumnRefs inside every sublink plan found in e — the retained
// SubPlans' correlation refs point at the pinned spine's outer layout,
// which join search just changed. Mirrors the MHJ path's use of
// remapOuterRefsInSubplan (joinlayout.go).
func remapSublinkOuterRefs(e Expr, pm func(int) int) {
	walkExprTree(e, func(x Expr) {
		switch s := x.(type) {
		case *ExistsExpr:
			remapOuterRefsInSubplan(s.Plan, 1, pm)
		case *InExpr:
			if s.Plan != nil {
				remapOuterRefsInSubplan(s.Plan, 1, pm)
			}
		case *SubqueryExpr:
			remapOuterRefsInSubplan(s.Plan, 1, pm)
		}
	})
}
