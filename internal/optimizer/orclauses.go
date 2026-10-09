package optimizer

import (
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// extractRestrictionOrClauses ports PG's orclauses.c
// (`extract_restriction_or_clauses`, called from query_planner): from each
// join OR clause, derive for every base relation it mentions an OR of the
// relation-only sub-clauses of its arms, and — when that rejects a useful
// fraction of the relation's rows — add it as a redundant base restriction.
// TPC-H Q19's three-way OR over part and lineitem gives part
// `(brand/container/size arm 1) OR (… arm 2) OR (… arm 3)`: PG then sizes part
// at ~200 rows and nested-loops into lineitem's index, where goopg sized part
// at 83333 and hash-joined (M0146-0005 slice 4).
//
// conjuncts are the join-residual conjuncts partitionConjunctsForJoinPlanning
// left for the search; the derived restriction for binding b is appended to
// locals.byBinding[b] (binding coordinates, localized later with the leaf's
// other quals). Only real FROM bindings (< nReal) receive one: the conjuncts
// the partition sees already exclude every nullable-side relation (the seam
// holds those above the prefix), which is PG's join_clause_is_movable_to for
// this flat inner pool.
//
// The returned map is PG's selectivity compensation: `consider_new_or_clause`
// divides the ORIGINAL join clause's cached norm_selec by the derived
// clause's selectivity, so the join's row estimate stays what it was without
// the redundant restriction. Deriving from the same clause for several
// relations divides once per relation, as PG's sequential updates of the one
// cached value do. joinClauseSelectivityExt applies it.
func extractRestrictionOrClauses(conjuncts []Expr, spans []leafSpan, bindings []rangeBinding, scans []Node,
	locals *relationLocalFilters, cat catalog.Catalog) map[Expr]float64 {
	var divisors map[Expr]float64
	for _, c := range conjuncts {
		bin, ok := c.(*BinaryOp)
		if !ok || bin.Op != parser.OpOr {
			continue
		}
		rs, attributable := relidsOfExpr(c, spans)
		if !attributable || relLevel(rs) < 2 {
			continue
		}
		for b := range bindings {
			if b >= len(scans) || scans[b] == nil || !relsOverlap(rs, RelSet(1)<<uint(b)) {
				continue
			}
			or := extractOrClauseFor(c, b, spans, cat)
			if or == nil {
				continue
			}
			sel := clauseSelectivity(localizeExprToLeaf(or, bindings[b]), scans[b])
			// "The clause is only worth adding to the query if it rejects a
			// useful fraction of the base relation's rows" — PG's threshold.
			if sel > 0.9 {
				continue
			}
			locals.byBinding[b] = append(locals.byBinding[b], or)
			if sel > 0 {
				if divisors == nil {
					divisors = make(map[Expr]float64)
				}
				if d, seen := divisors[c]; seen {
					divisors[c] = d * sel
				} else {
					divisors[c] = sel
				}
			}
		}
	}
	return divisors
}

// extractOrClauseFor is PG's extract_or_clause: from every arm of the OR,
// the AND of the sub-clauses that mention only binding b (recursing through a
// nested OR), OR-ed together — or nil when some arm yields nothing.
func extractOrClauseFor(or Expr, b int, spans []leafSpan, cat catalog.Catalog) Expr {
	var arms []Expr
	for _, arm := range flattenPlannerOr(or) {
		var subs []Expr
		for _, a := range splitAnd(arm) {
			if ab, ok := a.(*BinaryOp); ok && ab.Op == parser.OpOr {
				if sub := extractOrClauseFor(ab, b, spans, cat); sub != nil {
					subs = append(subs, sub)
				}
				continue
			}
			if isSafeRestrictionClauseFor(a, b, spans, cat) {
				subs = append(subs, a)
			}
		}
		if len(subs) == 0 {
			return nil
		}
		// Keep AND/OR flat: a lone OR sub-clause contributes its arms.
		sub := combineAnd(subs)
		if sb, ok := sub.(*BinaryOp); ok && sb.Op == parser.OpOr {
			arms = append(arms, flattenPlannerOr(sb)...)
		} else {
			arms = append(arms, sub)
		}
	}
	if len(arms) == 0 {
		return nil
	}
	out := arms[0]
	for _, a := range arms[1:] {
		out = &BinaryOp{Op: parser.OpOr, Left: out, Right: a}
	}
	return out
}

// isSafeRestrictionClauseFor is PG's is_safe_restriction_clause_for: the
// clause mentions binding b and only b, is not pseudoconstant (it mentions a
// relation at all), may sit on a leaf (no correlated sublink), and calls no
// volatile function.
func isSafeRestrictionClauseFor(c Expr, b int, spans []leafSpan, cat catalog.Catalog) bool {
	rs, attributable := relidsOfExpr(c, spans)
	if !attributable || rs != RelSet(1)<<uint(b) {
		return false
	}
	if !conjunctIsLocalEligible(c) {
		return false
	}
	return !exprListHasVolatileBuiltin([]Expr{c}, cat)
}
