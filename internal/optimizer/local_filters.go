package optimizer

import (
	"fmt"
	"sort"

	"github.com/goopg/goopg/internal/parser"
)

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
	return partitionConjunctsForJoinPlanningScoped(conjuncts, spans, false)
}

// partitionConjunctsForJoinPlanningScoped is partitionConjunctsForJoinPlanning
// with the scope's kind: scalarBody (M0146-0012) admits correlated conjuncts
// as base-rel restrictions in a multi-relation scalar sublink body too, as PG
// distributes a qual over a PARAM_EXEC Param to the one relation it reads.
func partitionConjunctsForJoinPlanningScoped(
	conjuncts []Expr,
	spans []leafSpan,
	scalarBody bool,
) (joinConjuncts []Expr, locals relationLocalFilters) {
	locals = relationLocalFilters{byBinding: make(map[int][]Expr)}
	for _, c := range conjuncts {
		// Conjuncts with subquery / outer-ref content can never be
		// safely pushed below a join boundary at this layer; the
		// existing planner stages own that work. Keep them in the
		// join-residual set.
		//
		// M0146-0015a: in a ONE-relation scope a correlated outer reference
		// does not disqualify. PG makes `x = outer.y` a base restriction
		// (the outer Var is a PARAM_EXEC Param with no relids —
		// distribute_qual_to_rels, initsplan.c), which is what lets
		// match_clause_to_indexcol bind it as the SubPlan's index key;
		// held above the search, a correlated SubPlan scanned its whole
		// relation per outer row (the regress `subselect` >1 h hang). A
		// multi-relation scope keeps the decline: the post-planning
		// EXISTS→ANY and unnest passes read the correlation off the body's
		// top qual holder, and a qual sunk to a leaf under the body's join
		// is invisible to them (TPC-DS Q35 lost its hashed ANY to a
		// per-row SubPlan) — ledgered.
		if !conjunctLocalEligibility(c, len(spans) == 1 || scalarBody) {
			if b := correlatedScalarSublinkLeaf(c, spans); b >= 0 {
				locals.byBinding[b] = append(locals.byBinding[b], c)
				continue
			}
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
	places := equivalenceClassPlaces(conjuncts)
	for b, cs := range locals.byBinding {
		locals.byBinding[b] = equivalenceClausesLast(cs, places)
	}
	return joinConjuncts, locals
}

// ecPlace is where generate_base_implied_equalities puts one equality:
// rank is its EquivalenceClass's position in root->eq_classes, and
// regenerated says the EC is rebuilt as `member = const` clauses rather than
// handing back the written one.
type ecPlace struct {
	rank        int
	regenerated bool
}

// equivalenceClassPlaces replays process_equivalence (equivclass.c) over a
// scope's conjuncts in order and returns, for each equality it accepts, the
// final position of its EquivalenceClass in root->eq_classes (M0146-0042).
// generate_base_implied_equalities walks that list in order, so a relation's
// EC-derived restrictions come out in EC creation order, not written order:
// in TPC-DS Q31 `ss2.d_year = 1999` joins the EC `ss1.d_year = 1999` opened
// before `ss2.d_qoy = 2`'s, and PG filters ss2 on `(d_year = 1999) AND
// (d_qoy = 2)`.
//
// The replay follows process_equivalence: a new EC is appended; an item found
// in an EC joins it (a constant matches an equal constant of the same type,
// so `o1.b = 2` joins the EC of an earlier `o2.b = 2`); when the two items
// sit in different ECs, the right one's merges into the left one's and
// leaves the list. Items are identified by exprIdentityKey; a constant's key
// carries its column's type, standing in for em_datatype.
//
// generate_base_implied_equalities_const re-uses the written clause only
// when the EC holds one `var = const` source and two members; otherwise it
// builds `member = const` for every member, so `3 = j2.c` in an EC that also
// holds `j1.c` prints `(c = 3)` (regress join.sql's self-join cases).
func equivalenceClassPlaces(conjuncts []Expr) map[Expr]ecPlace {
	type eqClass struct {
		merged  *eqClass
		members map[string]bool
		consts  map[string]bool
		sources int
	}
	resolve := func(ec *eqClass) *eqClass {
		for ec.merged != nil {
			ec = ec.merged
		}
		return ec
	}
	var list []*eqClass
	member := map[string]*eqClass{}
	clauseEC := map[Expr]*eqClass{}
	itemKey := func(e, other Expr) (string, bool) {
		k, ok := exprIdentityKey(e, scopeVeto)
		if !ok {
			return "", false
		}
		if col, isCol := other.(*ColumnRef); isCol && isPlainConstantBound(e) {
			k += "@" + col.Type.Name
		}
		return k, true
	}
	for _, c := range conjuncts {
		if !isEquivalenceClause(c) {
			continue
		}
		b := c.(*BinaryOp)
		k1, ok1 := itemKey(b.Left, b.Right)
		k2, ok2 := itemKey(b.Right, b.Left)
		if !ok1 || !ok2 || k1 == k2 {
			continue
		}
		ec1, ec2 := member[k1], member[k2]
		if ec1 != nil {
			ec1 = resolve(ec1)
		}
		if ec2 != nil {
			ec2 = resolve(ec2)
		}
		var ec *eqClass
		switch {
		case ec1 != nil && ec2 != nil:
			ec = ec1
			if ec2 != ec1 {
				for k := range ec2.members {
					ec1.members[k] = true
				}
				for k := range ec2.consts {
					ec1.consts[k] = true
				}
				ec1.sources += ec2.sources
				ec2.merged = ec1
				for i, x := range list {
					if x == ec2 {
						list = append(list[:i], list[i+1:]...)
						break
					}
				}
			}
		case ec1 != nil:
			ec = ec1
		case ec2 != nil:
			ec = ec2
		default:
			ec = &eqClass{members: map[string]bool{}, consts: map[string]bool{}}
			list = append(list, ec)
		}
		member[k1], member[k2] = ec, ec
		ec.members[k1], ec.members[k2] = true, true
		if isPlainConstantBound(b.Left) {
			ec.consts[k1] = true
		}
		if isPlainConstantBound(b.Right) {
			ec.consts[k2] = true
		}
		ec.sources++
		clauseEC[c] = ec
	}
	pos := make(map[*eqClass]int, len(list))
	for i, ec := range list {
		pos[ec] = i
	}
	places := make(map[Expr]ecPlace, len(clauseEC))
	for c, ec := range clauseEC {
		ec = resolve(ec)
		places[c] = ecPlace{
			rank:        pos[ec],
			regenerated: len(ec.consts) == 1 && (len(ec.members) != 2 || ec.sources != 1),
		}
	}
	return places
}

// equivalenceClausesLast reorders one relation's restriction list the way
// PG's baserestrictinfo comes out (M0146-0005co). distribute_qual_to_rels
// keeps an equality that process_equivalence accepts (`t_hour = 8`, or two
// columns of the relation equated) out of the list, and
// generate_base_implied_equalities appends it back after every other qual;
// order_qual_clauses' stable cost sort leaves that order alone when the
// costs tie. So `t_hour = 8 AND t_minute >= 30` filters as
// `(t_minute >= 30) AND (t_hour = 8)`. The partition is stable; the cost
// sort for unequal costs is not modelled. places (equivalenceClassPlaces)
// orders the equalities by their EC's position, as
// generate_base_implied_equalities emits them, and turns a regenerated
// `const = col` into PG's `col = const`; an equality with no place keeps its
// written place after the placed ones.
func equivalenceClausesLast(cs []Expr, places map[Expr]ecPlace) []Expr {
	var rest, ec []Expr
	for _, c := range cs {
		if isEquivalenceClause(c) {
			ec = append(ec, c)
		} else {
			rest = append(rest, c)
		}
	}
	rank := func(c Expr) int {
		if pl, ok := places[c]; ok {
			return pl.rank
		}
		return len(places)
	}
	sort.SliceStable(ec, func(i, j int) bool { return rank(ec[i]) < rank(ec[j]) })
	for i, c := range ec {
		if b := c.(*BinaryOp); places[c].regenerated && isPlainConstantBound(b.Left) {
			flipped := *b
			flipped.Left, flipped.Right = b.Right, b.Left
			ec[i] = &flipped
		}
	}
	if len(rest)+len(ec) < 2 {
		return append(rest, ec...)
	}
	// order_qual_clauses: a stable sort by per-tuple evaluation cost
	// (qualEvalOps, cost_qual_eval's count), so a cheap equality still
	// precedes a costlier OR or IN list.
	return orderQualClauses(append(rest, ec...))
}

// isEquivalenceClause reports whether c is an `=` whose two sides are a
// column and a constant, or two columns — the shape process_equivalence
// turns into an EquivalenceClass member pair. Written as type assertions
// (no new Expr switch site for the walker census).
func isEquivalenceClause(c Expr) bool {
	b, ok := c.(*BinaryOp)
	if !ok || b.Op != parser.OpEq {
		return false
	}
	lc, rc := isPlainConstantBound(b.Left), isPlainConstantBound(b.Right)
	_, lcol := b.Left.(*ColumnRef)
	_, rcol := b.Right.(*ColumnRef)
	return (lcol && (rc || rcol)) || (rcol && lc)
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
	return conjunctLocalEligibility(e, false)
}

// conjunctLocalEligibility is conjunctIsLocalEligible with the OuterColumnRef
// decline switchable: admitOuterRefs admits a correlated outer reference
// (partitionConjunctsForJoinPlanning's one-relation scope, M0146-0015a).
// Every other decline is unchanged. localizeExprToLeaf leaves an
// OuterColumnRef untouched (it names a scope above), tableForCol ignores it,
// and isParallelSafeExpr keeps a leaf carrying one off partial paths — as
// PG's parallel-restricted Param does.
func conjunctLocalEligibility(e Expr, admitOuterRefs bool) bool {
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
				if admitOuterRefs {
					return true
				}
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
			// M0146-0130: a host-level sublink keeps its inner plan's
			// FROM-cumulative references and runs against the leaf row
			// padded back to them (OuterRowPad).
			if binding.offset > 0 {
				if sq, isSq := n.(*SubqueryExpr); isSq && sq.Plan != nil {
					sq.OuterRowPad += binding.offset
				}
				if ex, isEx := n.(*ExistsExpr); isEx && ex.Plan != nil {
					ex.OuterRowPad += binding.offset
				}
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

// correlatedScalarSublinkLeaf returns the binding a conjunct holding a
// correlated scalar sublink restricts, or -1 (M0146-0005bu). PG distributes
// a qual by the relids of its Vars, and a correlated SubPlan's Vars are its
// testexpr's plus the outer Vars its parameters carry
// (distribute_qual_to_rels over pull_varnos, initsplan.c): when all of them
// name one relation the whole clause is that relation's base restriction.
// TPC-DS Q1/Q30/Q81 filter `ctr1.ctr_total_return > (SELECT avg(...) FROM
// ctr2 WHERE ctr1.k = ctr2.k)` on the ctr1 CTE Scan; goopg held it above the
// whole join.
//
// Admitted: scalar sublinks, and EXISTS sublinks that are not the conjunct
// itself (M0146-0005dx1), every inner-plan outer reference naming this scope
// (none reaching past it) and every such reference plus every same-scope
// column inside ONE binding that starts at offset 0. Offset 0 is the one
// binding whose leaf coordinates ARE the FROM-cumulative ones, so neither
// localizeExprToLeaf nor the inner plan (whose references stay unrebased —
// the rebase is ledgered) moves a coordinate.
//
// An EXISTS that IS the conjunct (or its NOT) stays above: it is the
// post-planning unnest pass's semi/anti join. One under an OR can never
// become a join; PG keeps TPC-DS Q10/Q35's `(EXISTS (… ws …) OR EXISTS
// (… cs …))` on the customer scan, where cost_qual_eval prices it per row by
// the plain correlated SubPlan (qualEvalOps' subPlanCostOps), and the
// post-planning EXISTS->ANY pass still converts it there, reading its host
// row off the leaf's Filter.
func correlatedScalarSublinkLeaf(c Expr, spans []leafSpan) int {
	if len(spans) < 2 || anySublinkPullupCandidate(c) {
		return -1
	}
	if b := correlatedScalarSublinkBinding(c, spans, 0); b == 0 {
		return 0
	}
	// M0146-0130: any other binding, whose leaf coordinates are offset —
	// localizeExprToLeaf pads the sublink's host row back to the inner
	// plan's FROM-cumulative references (OuterRowPad), so the inner plan is
	// not rebased. TPC-DS Q6's `i.i_current_price > 1.2 * (SELECT avg(...)
	// FROM item j WHERE j.i_category = i.i_category)` restricts `item i`,
	// the fifth FROM item.
	//
	// The conjunct must read a column of that binding itself, outside the
	// sublink: the seam's outer-join guard attributes a conjunct by its own
	// columns (relidsOfExpr), so only then does it see — and hold above the
	// join — a qual on an outer join's nullable side. A conjunct whose only
	// Vars are the sublink's outer references keeps the first-binding rule,
	// whose relation is never nullable (regress join's `1 = (SELECT 1 …
	// WHERE ss.y IS NOT NULL)` over `t1 LEFT JOIN ss` must stay above the
	// join).
	b := tableForCol(c, spans)
	if b <= 0 {
		return -1
	}
	if correlatedScalarSublinkBinding(c, spans, b) == b {
		return b
	}
	return -1
}

// correlatedScalarSublinkBinding is correlatedScalarSublinkLeaf's test for
// one candidate binding b: b, or -1.
func correlatedScalarSublinkBinding(c Expr, spans []leafSpan, b int) int {
	// The same-scope columns name binding b — or there are none, as in
	// Q10's OR of two EXISTS, whose only Vars are the SubPlans' outer
	// references, checked below.
	if tableForCol(c, spans) != b {
		sameScope := false
		visitColumnRefsForTable(c, func(int) { sameScope = true })
		if sameScope {
			return -1
		}
	}
	top := c
	if u, isNot := c.(*UnaryOp); isNot && u.Op == parser.OpNot {
		top = u.Operand
	}
	if _, isExists := top.(*ExistsExpr); isExists {
		return -1
	}
	lo, hi := spans[b].lo, spans[b].hi
	// planEscapesBy's walk: a reference past this scope escapes as ever, and
	// one naming this scope escapes the leaf unless binding 0 holds it.
	outside := func(o *OuterColumnRef, depth int) bool {
		return o.Level > depth || (o.Level == depth && (o.Index < lo || o.Index >= hi))
	}
	scalar, ok := false, true
	walked := walkExprRefs(c, scopeSignal, exprVisitor{
		Visit: func(n Expr) bool {
			switch x := n.(type) {
			case *OuterColumnRef:
				ok = false
			case *SubqueryExpr:
				if x.Plan == nil || len(x.Args) > 0 || len(x.ParParam) > 0 ||
					planEscapesBy(x.Plan, 1, outside) {
					ok = false
				}
				scalar = true
			case *InExpr:
				if x.Plan != nil {
					ok = false
				}
			case *ExistsExpr:
				// An offset binding admits scalar sublinks only: the
				// EXISTS→ANY pass rewrites an EXISTS into an InExpr, which
				// carries no OuterRowPad (M0146-0130, ledgered).
				if x.Plan == nil || len(x.Args) > 0 || len(x.ParParam) > 0 ||
					planEscapesBy(x.Plan, 1, outside) || b > 0 {
					ok = false
				}
				scalar = true
			case *ArraySubqueryExpr, *MultiAssignSubqRow, *MultiAssignSubqElem:
				ok = false
			}
			return ok
		},
	})
	if !walked || !ok || !scalar {
		return -1
	}
	return b
}
