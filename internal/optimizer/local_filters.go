package optimizer

import "fmt"

// M0077-0001 (Slice A): relation-local predicate
// partition + leaf-local rebasing.
//
// This file is the planner-side first slice of the
// 4-slice Q5 fix from `docs/design/fix-for-q5/`.
// What survives of it after M0127-P6.3:
//
//  1. Partition WHERE conjuncts into "join-side"
//     (multi-binding) vs "relation-local" (one-binding)
//     buckets BEFORE the join-order search runs
//     (`partitionConjunctsForJoinPlanning` — the PG-shaped
//     seam consumes it at joinsearchseam.go).
//  2. Rebase a local conjunct from FROM-cumulative to
//     leaf-local coordinates (`localizeExprToLeaf` — the
//     seam and `estimateBaseRelInfo` both consume it).
//
// The two functions that attached the partitioned locals to the tree the
// old subset-bitmask DP had picked — `shouldAttachLocalFiltersBeforeSearch`
// (the Slice-A rollout gate) and `attachRelationLocalFilters` (the
// pointer-identity leaf wrapper) — were deleted at M0127-P6.3 with that DP,
// their only production caller (08 §4). The PG-shaped search attaches locals
// to the leaf BEFORE the search instead (joinsearchseam.go), which is the
// shape its index producers expect and removes the pointer-identity
// dependency the post-hoc attach needed.
//
// Slice B refines leaf cardinality with reliable-
// selectivity from the M0077-0002 `baseRelInfo` work;
// Slice C+D continue with cost-model + anchored
// equality synthesis. See
// `docs/design/fix-for-q5/01-target-shape-and-local-filtering.md`.

// relationLocalFilters carries the per-binding local
// predicates extracted by partitionConjunctsForJoinPlanning.
// Indexed by binding position in the FROM clause (NOT
// scan-output offset).
type relationLocalFilters struct {
	byBinding map[int][]Expr
}

// partitionConjunctsForJoinPlanning splits a flat
// conjunct list into two sets:
//
//  1. joinConjuncts — multi-binding predicates plus
//     any conjunct containing OuterColumnRef /
//     SubqueryExpr / ExistsExpr / InExpr-with-Plan
//     (per design 01 §3.1). These flow into the join
//     search / Filter residual path unchanged.
//  2. locals — one-binding predicates keyed by binding
//     index. The PG-shaped seam attaches these to the
//     corresponding leaf scan BEFORE the search runs
//     (joinsearchseam.go).
//
// `spans` maps the FROM-cumulative output column offsets to each leaf's
// (lo, hi) range — the per-leaf table `tableForCol`'s contract now uses
// (joinrestrict.go, M0142-0008a-3i-plumbing-b2 design doc §25.3/§26).
//
// (M0077-0001.)
func partitionConjunctsForJoinPlanning(
	conjuncts []Expr,
	spans []leafSpan,
) (joinConjuncts []Expr, locals relationLocalFilters) {
	locals = relationLocalFilters{byBinding: make(map[int][]Expr)}
	for _, c := range conjuncts {
		// Conjuncts with subquery / outer-ref content can never be
		// safely pushed below a join boundary at this layer; the
		// existing planner stages own that work. Keep them in the
		// join-residual set.
		if !conjunctIsLocalEligible(c) {
			joinConjuncts = append(joinConjuncts, c)
			continue
		}
		bidx := tableForCol(c, spans)
		if bidx < 0 {
			// Multi-binding (or ColumnRef-out-of-range — rare but
			// possible during error recovery). Treat as join-side.
			joinConjuncts = append(joinConjuncts, c)
			continue
		}
		locals.byBinding[bidx] = append(locals.byBinding[bidx], c)
	}
	return joinConjuncts, locals
}

// conjunctIsLocalEligible reports whether the expression
// is structurally safe to attach as a `Filter(leaf)`
// wrapper inside the bushy DP's leaf set. Per design 01
// §3.1.3: any conjunct containing OuterColumnRef,
// SubqueryExpr, ExistsExpr, or InExpr with Plan != nil
// is INELIGIBLE — those nodes carry execution-time
// dependencies that the leaf attachment cannot honour.
//
// M0125-0002 commit 6 (with localizeExprToLeaf — producer and
// consumer land together): built on walkExprRefs / exprChildSlots
// instead of its own 9-of-32 type switch. The old switch had no
// default and only descended BinaryOp / UnaryOp / FuncCall /
// CaseExpr / ExtractExpr / InExpr, so a conjunct whose subquery or
// outer reference sat under any OTHER container — `(x IS NULL) =
// true`, `CAST(x AS int) > (subq)`, `ROW(x, (subq))`, `x IS
// DISTINCT FROM (subq)`, a COLLATE, an IS TRUE — produced zero
// callbacks below that container and returned a VACUOUS true. That
// is the fail-open direction: the conjunct was moved out of
// joinConjuncts into locals and pushed to a leaf, where
// localizeExprToLeaf (equally incomplete) left its ColumnRef
// indices in FROM-cumulative coordinates. Completing the pair
// therefore REMOVES predicates from the leaf-local set rather than
// adding any.
//
// Scope policy: scopeSignal since M0146-0002e — the Visit arm decides
// each sublink (only an uncorrelated one is admitted; see
// sublinkIsUncorrelated). An unenumerated type still aborts, which turns
// the old silent admission into a decline (fail closed). Declining costs
// an optimisation — the conjunct stays in the join residual and is
// evaluated above the join — never a wrong answer, so unlike commits 3/4
// this walker must NOT panic on an unknown type.
//
// (M0077-0001.)
func conjunctIsLocalEligible(e Expr) bool {
	if anySublinkPullupCandidate(e) {
		return false
	}
	eligible := true
	// scopeSignal, not scopeVeto (M0146-0002e): an inner plan is no longer
	// declined by the walker itself. The Visit arm below decides per sublink,
	// and anything it does not admit still declines — so the only shapes the
	// scope change lets through are the ones sublinkIsUncorrelated proves.
	ok := walkExprRefs(e, scopeSignal, exprVisitor{
		Visit: func(n Expr) bool {
			switch x := n.(type) {
			case *OuterColumnRef:
				// A childless leaf: exprChildSlots reports no slots
				// for it, so scopeVeto can never fire on its behalf
				// and the decline has to be explicit. Same shape as
				// commit 5's exprSide veto.
				eligible = false
				return false
			case *InExpr:
				// M0146-0002e: PG distributes a restriction by the relids of
				// its Vars (distribute_qual_to_rels, initsplan.c), and an
				// uncorrelated SubPlan contributes none — TPC-H Q16's
				// `ps_suppkey NOT IN (SELECT s_suppkey …)` is a partsupp
				// base restriction there. An uncorrelated sublink's only
				// same-scope columns are its testexpr's, which
				// localizeExprToLeaf rebases; a correlated one's inner plan
				// addresses this scope's columns and would need
				// remapOuterRefsInSubplan, so it keeps the historical
				// decline (ledgered). A literal IN list (no Plan) is an
				// ordinary same-scope expression.
				if x.Plan != nil && !sublinkIsUncorrelated(x.Plan, x.Args, x.ParParam) {
					eligible = false
					return false
				}
				return true
			case *SubqueryExpr:
				// Same rule as *InExpr; an unplanned node has no proof.
				if !sublinkIsUncorrelated(x.Plan, x.Args, x.ParParam) {
					eligible = false
					return false
				}
				return true
			case *ExistsExpr:
				if !sublinkIsUncorrelated(x.Plan, x.Args, x.ParParam) {
					eligible = false
					return false
				}
				return true
			case *ArraySubqueryExpr, *MultiAssignSubqRow, *MultiAssignSubqElem:
				// They carry the same execution-time dependency and were
				// admitted by accident before design 01 §3.1.3's rule was
				// completed; no restriction PG distributes takes these forms
				// in goopg's corpus, so they stay declined.
				eligible = false
				return false
			}
			return true
		},
	})
	// ok == false means the walk ABORTED on a type exprChildSlots does not
	// know. That is a decline — the old switch had no default, so a
	// conjunct built entirely from unenumerated kinds returned a vacuous
	// true and was pushed to a leaf that localizeExprToLeaf could not
	// rebase.
	return eligible && ok
}

// anySublinkPullupCandidate reports whether a conjunct is a bare ANY sublink
// — `x IN (SELECT …)`, `x op ANY (SELECT …)` — which PG does not keep as a
// restriction at all: pull_up_sublinks_qual_recurse converts a top-level
// ANY_SUBLINK into a semi join (convert_ANY_sublink_to_join,
// prepjointree.c:665, subselect.c:1333), correlated or not. goopg's
// stand-ins for that conversion are the jointree ANY pull-up and, for the
// shapes it declines (a CTE or grouped body — TPC-DS Q95, TPC-H Q18), the
// legacy unnest; both consume the conjunct from the join residual, so it
// must stay there (M0146-0002e: admitting Q95's two `IN (… ws_wh)` as leaf
// filters bypassed the semi join and ran both SubPlans in every parallel
// worker — an SF1 timeout). `NOT IN` (Negated, `<> ALL`) and `op ALL` are
// ALL_SUBLINKs, and a sublink under NOT / OR / any other operator is not a
// top-level conjunct: PG leaves all of those as SubPlans in a distributed
// restriction, which is what the Visit arm admits.
func anySublinkPullupCandidate(e Expr) bool {
	in, ok := e.(*InExpr)
	return ok && in.Plan != nil && !in.Negated && !in.AllOp
}

// sublinkIsUncorrelated reports whether a sublink is a planned SubPlan that
// reads nothing from the enclosing scope, i.e. one whose result is the same
// for every row of the relation it filters. It must have a Plan (an
// unplanned node has no proof), no PARAM_EXEC arguments (Args are
// correlation values lowered out of the plan, so a lowered correlated
// sublink shows no OuterColumnRef in its plan yet is correlated), and no
// OuterColumnRef escaping the plan (planHasOuterRef, binder-aware).
func sublinkIsUncorrelated(plan Node, args []Expr, parParam []int) bool {
	return plan != nil && len(args) == 0 && len(parParam) == 0 && !planHasOuterRef(plan)
}

// localizeExprToLeaf rewrites every ColumnRef.Index in
// the expression from FROM-cumulative coordinates into
// leaf-local coordinates by subtracting binding.offset.
// SourceTableIdx is preserved unchanged (the relation
// identity doesn't change when we rebase).
//
// M0125-0002 commit 6: built on cloneExprRefs / exprChildSlots
// instead of its own 7-of-32 type switch, whose trailing
// pass-through ("Constants … no ColumnRef; pass through") was a
// claim about the seven kinds it knew and a silent lie about the
// other twenty-five: an IsNullExpr, CastExpr, RowExpr, IsBoolExpr,
// CollateExpr or IsDistinctFromExpr wrapping a ColumnRef was
// returned UNCHANGED, i.e. attached to a leaf Filter still carrying
// FROM-cumulative indices. With binding.offset > 0 that reads the
// wrong column at execution time. conjunctIsLocalEligible was the
// only thing keeping it rare, and it was fail-open in the same
// places — the two are one commit for that reason.
//
// Scope policy: scopeIgnore since M0146-0002e. conjunctIsLocalEligible
// admits a sublink only when its inner plan reads nothing from this
// scope (sublinkIsUncorrelated), so the plan holds no coordinate this
// rebase must move: only the sublink's same-scope slots (the IN
// operand) shift, exactly the testexpr columns PG's distributed
// restriction carries. The shallow clone shares the inner Plan, which
// is never mutated here. An abort can now only be a type
// exprChildSlots does not know — which the eligibility walk declines
// over the SAME primitive, so an abort means the pair has diverged,
// and that is a planner bug, not a shape this function may decline:
// the caller has already removed the conjunct from joinConjuncts, so
// returning it un-rebased (or dropping it) would be a wrong answer.
// Hence the panic.
//
// (M0077-0001.)
func localizeExprToLeaf(e Expr, binding rangeBinding) Expr {
	if e == nil {
		return nil
	}
	out, ok := cloneExprRefs(e, scopeIgnore, exprRewriter{
		Rewrite: func(n Expr) Expr {
			// n is the CLONE — cloneExprRefs shallow-copies every
			// node, leaves included — so mutating it in place leaves
			// the caller's tree untouched. That is the defensive copy
			// the old *ColumnRef arm made by hand, now uniform across
			// all 32 kinds instead of the 7 it enumerated.
			if cr, isCol := n.(*ColumnRef); isCol {
				cr.Index -= binding.offset
			}
			return n
		},
	})
	if !ok {
		panic(fmt.Sprintf("localizeExprToLeaf: cannot rebase %T — "+
			"conjunctIsLocalEligible must decline every conjunct this "+
			"driver aborts on (a type exprChildSlots does not know). The producer and the consumer "+
			"are one commit for exactly this reason; a silent pass-through "+
			"here leaves FROM-cumulative indices on a leaf-local Filter", e))
	}
	return out
}
