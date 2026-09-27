package optimizer

import (
	"strings"

	"github.com/goopg/goopg/internal/catalog"
)

// subquerypushdown.go — the aggregated arm of PG's subquery_push_qual
// (allpaths.c:4023, called from set_subquery_pathlist). PG moves a
// restriction clause that names only a derived table's output columns into
// the subquery itself — into havingQual when the subquery groups or
// aggregates, into its WHERE otherwise — so the qual prices and filters at
// the level it actually constrains. goopg plans the subquery eagerly at
// leaf binding (planSubqueryRangeVar), so the equivalent move at leaf-qual
// attach time is a *Filter spliced directly above the subquery's
// *Aggregate node — the same position a native HAVING's Filter takes
// (planner.go's buildAggregateStage site).
//
// Two consequences fall out of PG's own rules:
//
//   - The pushed qual evaluates against the aggregate's output row, so a
//     leaf-local ColumnRef must first be remapped through whatever
//     passthrough-in-ROWS-but-not-in-POSITIONS nodes sit between the
//     SubqueryScan label and the aggregate — chiefly the SELECT-list
//     *Project — the way upstream's ReplaceVarsFromTargetList substitutes
//     Var -> tlist expr. EXPLAIN's expandAggOutputRefsInFilter then renders
//     the reference as the underlying aggregate call, exactly like
//     upstream's rewritten HAVING (`Filter: (count(*) >= 15)`, not
//     `Filter: (cnt >= 15)`).
//   - With no qual left above the SubqueryScan, M0146-0005w's
//     triviality strip removes the wrapper whenever the enclosing scope's
//     consumption is the in-order identity — which is why PG's plan shows
//     the bare aggregate where an unpushed qual would keep a
//     `Subquery Scan on <alias>` line.
//
// This slice implements ONLY the HAVING arm: the chain below the
// SubqueryScan must bottom out at an *Aggregate with no GroupingSets,
// reached through *Project / *Sort / *IncrementalSort / *Filter wrappers
// only. The WHERE arm pushes into the subquery's own jointree, whose
// bindings no longer exist at leaf-attach time — pushing it there needs
// the subquery replanned with the qual, a separate slice (ledgered), as is
// upstream's non-Var targetlist substitution (a derived column that is a
// computed expression keeps its qual leaf-local here).

// subqueryLeafAggTarget finds the *Aggregate a pushed qual must evaluate
// against: the bottom of the run of *Filter wrappers (a native HAVING, or
// nothing else) that directly tops the *Aggregate. Returns nil for any
// other node, for a gap in the filter run, and for GroupingSets —
// matching upstream's `subquery->groupClause && subquery->groupingSets`
// refusal (allpaths.c point 6).
func subqueryLeafAggTarget(n Node) *Aggregate {
	for {
		f, ok := n.(*Filter)
		if !ok {
			break
		}
		if f.Child == nil {
			return nil
		}
		n = f.Child
	}
	agg, ok := n.(*Aggregate)
	if !ok || len(agg.GroupingSets) > 0 {
		return nil
	}
	return agg
}

// subqueryLeafPosToAggExpr resolves leaf output position pos to the
// expression it holds at the aggregate's output level — the
// ReplaceVarsFromTargetList step. *Filter, *Sort and *IncrementalSort are
// position passthroughs; a *Project maps position p through its target
// list. Only a plain *ColumnRef target may be followed (a computed target
// would need its own expression substituted, which upstream does but this
// slice declines). Every other node kind fails closed, as does an
// out-of-range target. The name desync guard lives in subqueryLeafRebase
// against the LEAF schema — the aggregate's own output name (e.g. `count`)
// legitimately differs from the leaf's alias (`cnt`) and is adopted onto
// the rebased ref so expandAggOutputRef's render guard admits it.
func subqueryLeafPosToAggExpr(n Node, pos int, orig *ColumnRef) (Expr, bool) {
	for {
		switch x := n.(type) {
		case *Filter:
			n = x.Child
		case *Sort:
			n = x.Child
		case *IncrementalSort:
			n = x.Child
		case *Project:
			if pos < 0 || pos >= len(x.Targets) {
				return nil, false
			}
			cr, ok := x.Targets[pos].(*ColumnRef)
			if !ok {
				return nil, false
			}
			pos = cr.Index
			n = x.Child
		case *Aggregate:
			if len(x.GroupingSets) > 0 || pos < 0 || pos >= len(x.Output()) {
				return nil, false
			}
			out := *orig
			out.Index = pos
			// The reference now evaluates on the aggregate's output row,
			// so it must carry the aggregate's own output name — a native
			// `HAVING count(*) > n` conjunct is bound exactly this way.
			// (The leaf-level alias, e.g. `cnt`, lives only on the
			// SubqueryScan's schema.) expandAggOutputRef's render guard
			// (`sch[idx].Name == col.Name`) then admits it, so the qual
			// prints PG's `count(*)` text.
			out.Name = x.Output()[pos].Name
			return &out, true
		default:
			return nil, false
		}
	}
}

// subqueryLeafRebase rewrites a leaf-local conjunct's ColumnRefs into
// aggregate-output coordinates via subqueryLeafPosToAggExpr. Any ref that
// cannot be resolved fails the whole conjunct — partial rewrites would
// silently mix coordinate spaces.
func subqueryLeafRebase(e Expr, leafSchema Schema, top Node) (Expr, bool) {
	sch := leafSchema
	bad := false
	out, ok := cloneExprRefs(e, scopeIgnore, exprRewriter{
		Rewrite: func(n Expr) Expr {
			cr, isCol := n.(*ColumnRef)
			if !isCol || bad {
				return n
			}
			// Desync guard: the localized ref's index/name must agree
			// with the leaf schema it was localized against before any
			// position is trusted.
			if cr.Index < 0 || cr.Index >= len(sch) ||
				(cr.Name != "" && sch[cr.Index].Name != "" &&
					!strings.EqualFold(sch[cr.Index].Name, cr.Name)) {
				bad = true
				return n
			}
			mapped, hit := subqueryLeafPosToAggExpr(top, cr.Index, cr)
			if !hit {
				bad = true
				return n
			}
			return mapped
		},
	})
	if bad || !ok {
		return nil, false
	}
	return out, true
}

// subqueryQualPushdownSafe is the narrow slice of qual_is_pushdown_safe
// (allpaths.c:3928) that applies once the leaf-local partition has already
// proven every column in the conjunct belongs to this leaf: no sublinks
// (PG's contain_subplans refusal — conjunctIsLocalEligibility ADMITS
// uncorrelated InExpr/SubqueryExpr/ExistsExpr conjuncts, so the refusal is
// real, not vestigial), no outer references (lateral columns are not
// pushable), and at least one leaf column (a pseudoconstant conjunct stays
// above the leaf, mirroring upstream's rinfo->pseudoconstant arm).
// Volatile conjuncts decline outright: upstream admits them into grouped
// subqueries, but keeping one leaf-local is never wrong and this slice
// stays fail-closed (ledgered).
func subqueryQualPushdownSafe(p Expr, cat catalog.Catalog) bool {
	hasCol := false
	bad := false
	// walkExprRefs under scopeVeto aborts on any inner-scope child — a
	// sublink's plan, the slotInnerPlan slot kinds — which is upstream's
	// contain_subplans refusal, fail-closed on unenumerated types too.
	// An unplanned `x IN (lit, lit)` list carries no scope, so it stays
	// pushable like any other same-scope expression.
	scoped := !walkExprRefs(p, scopeVeto, exprVisitor{
		Visit: func(e Expr) bool {
			if _, is := e.(*OuterColumnRef); is {
				bad = true
				return false
			}
			if _, is := e.(*ParamRef); is {
				bad = true
				return false
			}
			if _, is := e.(*ExecParamRef); is {
				bad = true
				return false
			}
			if _, is := e.(*CTIDExpr); is {
				bad = true
				return false
			}
			if _, is := e.(*TableOidExpr); is {
				bad = true
				return false
			}
			if ie, is := e.(*InExpr); is && ie.Plan != nil {
				bad = true
				return false
			}
			if _, is := e.(*ColumnRef); is {
				hasCol = true
			}
			return true
		},
	})
	if scoped || bad || !hasCol {
		return false
	}
	return !exprListHasVolatileBuiltin([]Expr{p}, cat)
}

// pushQualsIntoSubqueryLeaf sinks the pushdown-safe subset of a derived
// table leaf's local conjuncts into a *Filter directly above the
// subquery's aggregate — the plan-level equivalent of upstream appending
// the clause to subquery->havingQual. On success it returns a NEW
// *SubqueryScan sharing the original child subtree up to the splice point
// (the passthrough wrappers between them are shallow-copied to relink the
// Filter — the original subtree is never mutated, so a seam decline still
// hands the caller's fallback the pre-push tree), plus the conjuncts that
// did NOT push; the caller wraps those in the ordinary leaf Filter, so
// nothing is dropped or double-applied.
//
// The pushed Filter is stacked as the outermost of the aggregate-adjacent
// Filter run: EXPLAIN's stacked-Filter collapse leads with the inner
// predicate, so the pushed text prints after any native HAVING — the same
// order make_and_qual(havingQual, qual) produces upstream.
func pushQualsIntoSubqueryLeaf(ss *SubqueryScan, preds []Expr, b rangeBinding, cat catalog.Catalog) (*SubqueryScan, []Expr, bool) {
	var pushed, kept []Expr
	for _, p := range preds {
		if !subqueryQualPushdownSafe(p, cat) {
			kept = append(kept, p)
			continue
		}
		rebased, ok := subqueryLeafRebase(localizeExprToLeaf(p, b), ss.Output(), ss.Child)
		if !ok {
			kept = append(kept, p)
			continue
		}
		pushed = append(pushed, rebased)
	}
	if len(pushed) == 0 {
		return nil, preds, false
	}
	newChild, ok := spliceFilterAboveSubqueryAgg(ss.Child, combineAnd(pushed))
	if !ok {
		return nil, preds, false
	}
	out := *ss
	out.Child = newChild
	return &out, kept, true
}

// spliceFilterAboveSubqueryAgg rebuilds the passthrough chain between the
// SubqueryScan label and its aggregate, inserting `pushed` as the
// outermost Filter of the run that directly tops the *Aggregate. Wrappers
// are shallow-copied on the way down so the shared subtree is untouched.
// A chain that does not bottom at an *Aggregate behind *Filter wrappers —
// or a *Filter that tops a Project (a coordinate boundary, not a having
// position) — fails closed.
func spliceFilterAboveSubqueryAgg(n Node, pushed Expr) (Node, bool) {
	switch x := n.(type) {
	case *Project:
		c, ok := spliceFilterAboveSubqueryAgg(x.Child, pushed)
		if !ok {
			return nil, false
		}
		np := *x
		np.Child = c
		return &np, true
	case *Sort:
		c, ok := spliceFilterAboveSubqueryAgg(x.Child, pushed)
		if !ok {
			return nil, false
		}
		ns := *x
		ns.Child = c
		return &ns, true
	case *IncrementalSort:
		c, ok := spliceFilterAboveSubqueryAgg(x.Child, pushed)
		if !ok {
			return nil, false
		}
		ns := *x
		ns.Child = c
		return &ns, true
	default:
		// The aggregate-adjacent *Filter run or the bare *Aggregate.
		if subqueryLeafAggTarget(n) == nil {
			return nil, false
		}
		return &Filter{pos: n.Pos(), Child: n, Predicate: pushed}, true
	}
}
