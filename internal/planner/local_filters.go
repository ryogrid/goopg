package planner

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
// `cumOffsets` maps the FROM-cumulative output column
// offsets — index `i` maps to bindings[i]'s first
// output-column index; index `len(bindings)` is the
// total schema width. This matches `tableForCol`'s
// contract (joinrestrict.go).
//
// (M0077-0001.)
func partitionConjunctsForJoinPlanning(
	conjuncts []Expr,
	cumOffsets []int,
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
		bidx := tableForCol(c, cumOffsets)
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
// Scope policy: scopeVeto. An inner plan is precisely the
// execution-time dependency §3.1.3 declines, so the abort IS the
// answer; an unenumerated type aborts too, which turns the old
// silent admission into a decline (fail closed). Declining costs an
// optimisation — the conjunct stays in the join residual and is
// evaluated above the join — never a wrong answer, so unlike
// commits 3/4 this walker must NOT panic on an unknown type.
//
// (M0077-0001.)
func conjunctIsLocalEligible(e Expr) bool {
	eligible := true
	ok := walkExprRefs(e, scopeVeto, exprVisitor{
		Visit: func(n Expr) bool {
			switch n.(type) {
			case *OuterColumnRef:
				// A childless leaf: exprChildSlots reports no slots
				// for it, so scopeVeto can never fire on its behalf
				// and the decline has to be explicit. Same shape as
				// commit 5's exprSide veto.
				eligible = false
				return false
			case *SubqueryExpr, *ExistsExpr, *ArraySubqueryExpr,
				*MultiAssignSubqRow, *MultiAssignSubqElem:
				// Declined whether or not Plan is set. exprChildSlots
				// emits the slotInnerPlan slot ONLY when Plan != nil,
				// so scopeVeto alone would admit an unplanned subquery
				// node — and the old switch declined SubqueryExpr /
				// ExistsExpr unconditionally, which is the behaviour
				// design 01 §3.1.3 states. The three Array/MultiAssign
				// kinds join them: they carry the same execution-time
				// dependency and were admitted by accident before.
				eligible = false
				return false
			}
			return true
		},
	})
	// ok == false means the walk ABORTED: either a slotInnerPlan child
	// under scopeVeto (an InExpr/SubqueryExpr/ExistsExpr that carries a
	// Plan) or a type exprChildSlots does not know. Both are declines —
	// the old switch had no default, so a conjunct built entirely from
	// unenumerated kinds returned a vacuous true and was pushed to a
	// leaf that localizeExprToLeaf could not rebase.
	return eligible && ok
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
// Scope policy: scopeVeto, which is unreachable by construction:
// every conjunct arriving here has passed conjunctIsLocalEligible,
// whose own scopeVeto already declined inner plans and unknown
// types over the SAME primitive. An abort therefore means the pair
// has diverged, and that is a planner bug, not a shape this
// function may decline: the caller has already removed the conjunct
// from joinConjuncts, so returning it un-rebased (or dropping it)
// would be a wrong answer. Hence the panic.
//
// (M0077-0001.)
func localizeExprToLeaf(e Expr, binding rangeBinding) Expr {
	if e == nil {
		return nil
	}
	out, ok := cloneExprRefs(e, scopeVeto, exprRewriter{
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
			"driver aborts on (an inner plan under scopeVeto, or a type "+
			"exprChildSlots does not know). The producer and the consumer "+
			"are one commit for exactly this reason; a silent pass-through "+
			"here leaves FROM-cumulative indices on a leaf-local Filter", e))
	}
	return out
}
