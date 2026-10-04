package optimizer

// sublinkjoinclause.go — M0146-0012a slice A: a correlated scalar sublink's
// clause is a JOIN clause.
//
// PG makes a correlated SubPlan's outer references PARAM_EXEC Params whose
// `args` are this scope's Vars, so pull_varnos counts them toward the clause's
// relids (distribute_qual_to_rels, initsplan.c). TPC-H Q17's
// `l_quantity < (SELECT 0.2 * avg(l_quantity) FROM lineitem WHERE
// l_partkey = p_partkey)` therefore has relids {lineitem, part}: a join
// clause, evaluated as the Hash Join's Join Filter. goopg's relidsOfExpr does
// not see into the sublink, so the clause read {lineitem} only, could not be
// placed at the join, and was applied above the finished join tree.
//
// The rewrite below is the pre-lowering M0146-0015c slice 2 built for pulled
// bodies (rebaseQualKeptSubplans), run with no pulled body: every outer
// reference naming this scope becomes an ExecParamRef sentinel and an Arg
// ColumnRef in problem space. Args are same-scope children of the sublink, so
// relidsOfExpr counts them and translateToLayout re-bases them onto whichever
// join row the search places the clause at; lowerSubPlanParams renumbers the
// sentinels after planning.
//
// Scope of the slice: scalar sublinks only. An EXISTS or IN keeps its
// correlation in its body for the post-planning unnest and EXISTS→ANY passes,
// which read it there (a pre-lowered body has no OuterColumnRef left to read).

// preLowerSpanningScalarSublinks replaces, in place, each conjunct holding a
// correlated scalar sublink whose pre-lowered form references two or more
// relations with that pre-lowered form. A conjunct the rewrite cannot carry,
// or that stays on one relation, is left exactly as it was.
func preLowerSpanningScalarSublinks(conjuncts []Expr, spans []leafSpan, ctx *resolveContext) {
	if ctx == nil || len(spans) < 2 {
		return
	}
	// Problem space is the scope's own FROM layout only when nothing was
	// pulled up into it. A pulled-up FROM subquery's columns are no longer
	// where the sublink's outer reference recorded them (regress join's
	// `ss.y`, a placeholder for 42, would read t2.q2), and a pulled sublink
	// body widens the space; the pulled-body rebase (rebasePulledQual)
	// maps those, this pass does not.
	if len(ctx.pulledDerived) > 0 || (ctx.jtPullup != nil && ctx.jtPullup.nLeaves > 0) {
		return
	}
	for i, c := range conjuncts {
		if out, ok := preLowerSpanningScalarSublink(c, spans, ctx); ok {
			conjuncts[i] = out
		}
	}
}

func preLowerSpanningScalarSublink(c Expr, spans []leafSpan, ctx *resolveContext) (Expr, bool) {
	if !conjunctHasOnlyCorrelatedScalarSublinks(c) || anySublinkPullupCandidate(c) {
		return nil, false
	}
	cl, ok := cloneExprRefs(c, scopeIgnore, exprRewriter{Rewrite: func(n Expr) Expr { return n }})
	if !ok || cl == nil {
		return nil, false
	}
	if !rebaseQualKeptSubplans(cl, nil, nil, len(ctx.schema), ctx, nil, nil) {
		return nil, false
	}
	relids, ok := relidsOfExpr(cl, spans)
	if !ok || relLevel(relids) < 2 || !sublinkArgsNameBaseRelations(cl, ctx) {
		return nil, false
	}
	return cl, true
}

// sublinkArgsNameBaseRelations reports whether every pre-lowered Arg in e
// reads a base relation of this scope. A FROM subquery or CTE binding's
// slots are the synthesized relation's (regress join's `ss.y`, a
// placeholder for 42), while its leaf may by now be the flattened body's
// scan (`t2`), so a slot there is not the column the Arg names.
func sublinkArgsNameBaseRelations(e Expr, ctx *resolveContext) bool {
	good := true
	walkExprRefs(e, scopeIgnore, exprVisitor{
		Visit: func(n Expr) bool {
			x, isSub := n.(*SubqueryExpr)
			if !isSub {
				return good
			}
			for _, a := range x.Args {
				cr, isCol := a.(*ColumnRef)
				if !isCol {
					good = false
					break
				}
				b, found := emittingBindingAt(ctx, cr.Index)
				if !found || b.rtid == 0 || b.cteRef {
					good = false
					break
				}
			}
			return good
		},
	})
	return good
}

// conjunctHasOnlyCorrelatedScalarSublinks reports whether c carries at least
// one correlated, not-yet-lowered scalar sublink, no other sublink kind, and
// no scalar sublink the post-planning unnest pass would decorrelate: that pass
// reads the correlation off the body's OuterColumnRefs, which pre-lowering
// replaces. goopg still decorrelates a body that is not probe-cheap
// (canUnnestSubquery) where PG keeps the SubPlan — ledgered; this slice
// leaves that choice exactly as it was.
func conjunctHasOnlyCorrelatedScalarSublinks(c Expr) bool {
	scalar, other := false, false
	ok := walkExprRefs(c, scopeIgnore, exprVisitor{
		Visit: func(n Expr) bool {
			if x, isSub := n.(*SubqueryExpr); isSub {
				if x.Plan == nil || len(x.Args) > 0 || len(x.ParParam) > 0 || !planHasOuterRef(x.Plan) ||
					(subqueryUnnestEnabled() && canUnnestSubquery(x)) {
					other = true
				} else {
					scalar = true
				}
			} else if len(ExprSubplans(n)) > 0 {
				other = true
			}
			return !other
		},
		OnUnknown: func(Expr) { other = true },
	})
	return ok && scalar && !other
}

// exprCarriesSublink reports whether e holds a sublink at this scope. Such a
// clause is never a hash or merge key: goopg's join executors evaluate keys
// without the SubPlan's parameter binding (PG can hash on `(SubPlan 1) =
// ps_supplycost`, TPC-H Q2 — ledgered).
func exprCarriesSublink(e Expr) bool {
	found := false
	walkExprRefs(e, scopeIgnore, exprVisitor{
		Visit: func(n Expr) bool {
			if len(ExprSubplans(n)) > 0 {
				found = true
			}
			return !found
		},
	})
	return found
}
