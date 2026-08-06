package planner

import "strings"

// pushQualsThroughSingleRefCTEs carries a restriction that sits on a
// CTE reference's OUTPUT through the reference and into the CTE body,
// for CTEs the statement references exactly once.
//
// This is goopg's analogue of the composition PG 12+ applies to the
// same shape: `inline_cte` (postgres/src/backend/optimizer/plan/
// subselect.c) turns a single-reference CTE into an ordinary sub-select,
// and subquery qual pushdown (allpaths.c: subquery_push_qual guarded by
// qual_is_pushdown_safe) then moves an outer restriction below the
// sub-select's GROUP BY when it references only plain grouping columns.
// TPC-DS Q78 is the witness: `ss_sold_year = 1998` reached `CTE Scan on
// ss` in M0125-0035 arm (a), but that filters the CTE's output AFTER
// the body's aggregate — all three channel CTEs still aggregated every
// year. Only inside the body can the restriction keep descending to the
// `date_dim` scan (via pushConjunctIntoSubtree, which this pass
// delegates to once it is below the body's projection/aggregate
// layers).
//
// Why refcount==1 is the gate, and why it is read only from Plan()'s
// tail: a CTE's body Node is SHARED between its references
// (planScanRangeVar wraps the same ce.body per consumer), and the
// executor additionally materializes the first reference's rows into
// the declaration-keyed ctx.CTERowCache which later references REPLAY
// (operators_cte_dml.go). Pushing one reference's restriction into a
// shared body would therefore filter every other reference twice over
// — once through the mutated plan, once through the poisoned cache.
// With exactly one reference both sharing channels are vacuous: the
// body runs once, for that reference, and its cache entry is never
// replayed. TPC-DS Q31's `ws3` (referenced 6×) is the control that
// must keep its per-reference filters on the CTE Scan output, which is
// where arm (a) left them. plannedCTE.refs is exact-or-overcounted
// (see planScanRangeVar), and it is final only after the whole
// statement is planned — an inner planSelect scope could observe
// refs==1 while a later sibling subquery still adds a second reference
// — hence the single call site in Plan().
//
// What PG declines that goopg cannot: inline_cte refuses a body
// containing volatile functions, because pushing a qual below it
// changes how many rows the volatile expression is evaluated for.
// goopg's planner has no volatility model (same gap innerJoinPushTarget
// records), so a restriction pushed into a single-reference body may
// reduce a volatile body expression's evaluation count. Deferred with
// a ledger row; the pushed conjunct itself never contains a FuncCall
// (vetoed in the remap).
func pushQualsThroughSingleRefCTEs(n Node) {
	if n == nil {
		return
	}
	switch x := n.(type) {
	case *Explain:
		pushQualsThroughSingleRefCTEs(x.Child)
	case *Filter:
		pushFilterQualsThroughCTEScan(x)
		pushQualsThroughSingleRefCTEs(x.Child)
	case *Join:
		pushQualsThroughSingleRefCTEs(x.Left)
		pushQualsThroughSingleRefCTEs(x.Right)
	case *NestedLoopIndexJoin:
		pushQualsThroughSingleRefCTEs(x.Outer)
		pushQualsThroughSingleRefCTEs(x.Inner)
	case *MultiHashJoin:
		for _, t := range x.Tables {
			pushQualsThroughSingleRefCTEs(t)
		}
	case *CTEScan:
		pushQualsThroughSingleRefCTEs(x.Child)
	case *CTEDMLPrefix:
		pushQualsThroughSingleRefCTEs(x.Body)
	case *Project:
		pushQualsThroughSingleRefCTEs(x.Child)
	case *Sort:
		pushQualsThroughSingleRefCTEs(x.Child)
	case *Limit:
		pushQualsThroughSingleRefCTEs(x.Child)
	case *Aggregate:
		pushQualsThroughSingleRefCTEs(x.Child)
	case *WindowAgg:
		pushQualsThroughSingleRefCTEs(x.Child)
	case *Distinct:
		pushQualsThroughSingleRefCTEs(x.Child)
	case *DistinctOn:
		pushQualsThroughSingleRefCTEs(x.Child)
	case *SetOp:
		pushQualsThroughSingleRefCTEs(x.Left)
		pushQualsThroughSingleRefCTEs(x.Right)
	case *RecursiveUnion:
		pushQualsThroughSingleRefCTEs(x.Anchor)
		pushQualsThroughSingleRefCTEs(x.Recursive)
	case *Gather:
		pushQualsThroughSingleRefCTEs(x.Child)
	case *GatherMerge:
		pushQualsThroughSingleRefCTEs(x.Child)
	case *LockRows:
		pushQualsThroughSingleRefCTEs(x.Child)
	}
}

// pushFilterQualsThroughCTEScan applies the transformation described on
// pushQualsThroughSingleRefCTEs to one Filter-over-CTEScan pair. Each
// conjunct is DUPLICATED into the body (property 2 of the join pass:
// the residual Filter keeps its copy, so a decline anywhere degrades to
// today's post-aggregate filtering, never to a dropped qual).
func pushFilterQualsThroughCTEScan(f *Filter) {
	if f == nil || f.Predicate == nil {
		return
	}
	scan, ok := f.Child.(*CTEScan)
	if !ok || scan.cte == nil || !scan.cte.inlineEligible || scan.cte.refs != 1 || scan.Child == nil {
		return
	}
	for _, c := range splitAnd(f.Predicate) {
		mapped, ok := remapConjunctThroughCTEOutput(c, scan.Output(), scan.Child.Output())
		if !ok {
			continue
		}
		if repl, ok := pushConjunctIntoCTEBody(scan.Child, mapped); ok {
			scan.Child = repl
		}
	}
}

// remapConjunctThroughCTEOutput re-expresses a conjunct given in the
// CTE reference's output space into the body's output space. Positions
// are identical on both sides — a CTE's `(col, ...)` alias list renames
// columns without moving them (preplanWithClause) — so only the NAMES
// change, and the positional name validation of the join pass's
// property 1 is applied against the CTE schema before the rewrite.
// FuncCall is vetoed for the same reason as innerJoinPushTarget (no
// volatility model; the conjunct is evaluated once per body row AND
// once per CTE output row), OuterColumnRef because its slot belongs to
// the enclosing statement's scope, not the body's.
func remapConjunctThroughCTEOutput(c Expr, cteOut, bodyOut Schema) (Expr, bool) {
	if len(cteOut) != len(bodyOut) {
		return nil, false
	}
	sawRef := false
	bad := false
	okWalk := walkExprRefs(c, scopeVeto, exprVisitor{
		Visit: func(e Expr) bool {
			if bad {
				return false
			}
			switch x := e.(type) {
			case *OuterColumnRef, *FuncCall:
				_ = x
				bad = true
				return false
			case *ColumnRef:
				if x.Index < 0 || x.Index >= len(cteOut) {
					bad = true
					return false
				}
				if x.Name != "" && !strings.EqualFold(cteOut[x.Index].Name, x.Name) {
					bad = true
					return false
				}
				sawRef = true
			}
			return true
		},
	})
	if !okWalk || bad || !sawRef {
		return nil, false
	}
	return cloneExprRefs(c, scopeVeto, exprRewriter{
		Rewrite: func(e Expr) Expr {
			if cr, ok := e.(*ColumnRef); ok {
				cr.Name = bodyOut[cr.Index].Name
			}
			return e
		},
	})
}

// pushConjunctIntoCTEBody carries a conjunct — expressed in n's OWN
// output space — down through the body's projection/aggregate layers,
// then hands it to pushConjunctIntoSubtree for the join-tree descent.
// Each layer either remaps the conjunct into its child's coordinate
// space (Project, Aggregate) or passes it through unchanged (Sort, and
// a Filter whose child is another of this pass's layers — the HAVING
// position, with which a surviving group-key restriction commutes).
// Limit declines: a filter below LIMIT/OFFSET changes which rows are
// kept. Anything unrecognized falls through to pushConjunctIntoSubtree,
// which is itself fail-closed.
func pushConjunctIntoCTEBody(n Node, c Expr) (Node, bool) {
	switch x := n.(type) {
	case *Project:
		mapped, ok := remapConjunctThroughProjection(c, x.Output(), x.Targets, x.Child.Output())
		if !ok {
			return n, false
		}
		repl, ok := pushConjunctIntoCTEBody(x.Child, mapped)
		if ok {
			x.Child = repl
		}
		return x, ok

	case *Aggregate:
		// Only a plain whole-input aggregate is understood; a parallel
		// split's Partial/Finalize halves and any Passthrough columns
		// change the output layout this remap assumes
		// ([GroupExprs...] ++ [Aggs...], buildAggregateStage).
		if x.Mode != AggModeSimple || x.PartialSource != nil || len(x.Passthrough) != 0 {
			return n, false
		}
		out := x.Output()
		if len(out) != len(x.GroupExprs)+len(x.Aggs) {
			return n, false
		}
		// Crossing below the aggregate is safe only when every
		// referenced output column is a plain grouping KEY: restricting
		// a group key removes whole groups and leaves every surviving
		// group's membership — and therefore every aggregate value —
		// untouched (PG qual_is_pushdown_safe / check_output_expressions).
		// A reference at or past len(GroupExprs) is an aggregate result
		// and remapConjunctThroughProjection declines it by bounds.
		mapped, ok := remapConjunctThroughProjection(c, out, x.GroupExprs, x.Child.Output())
		if !ok {
			return n, false
		}
		repl, ok := pushConjunctIntoCTEBody(x.Child, mapped)
		if ok {
			x.Child = repl
		}
		return x, ok

	case *Filter:
		switch x.Child.(type) {
		case *Project, *Aggregate, *Sort, *Filter:
			repl, ok := pushConjunctIntoCTEBody(x.Child, c)
			if ok {
				x.Child = repl
			}
			return x, ok
		}
		// Join tree or terminal below: pushConjunctIntoSubtree's Filter
		// case owns this shape (exprEqual idempotence guard, LeafLocal
		// convention check).
		return pushConjunctIntoSubtree(x, c)

	case *Sort:
		repl, ok := pushConjunctIntoCTEBody(x.Child, c)
		if ok {
			x.Child = repl
		}
		return x, ok

	case *Limit:
		return n, false
	}
	return pushConjunctIntoSubtree(n, c)
}

// remapConjunctThroughProjection re-expresses a conjunct given in a
// projection layer's output space into that layer's child space. exprs
// is the layer's output-position→expression mapping (Project.Targets,
// or Aggregate.GroupExprs — whose prefix positions are the group keys);
// a referenced position must map to a plain *ColumnRef or the conjunct
// declines. selfOut/childOut supply the positional name validation at
// BOTH hops: the conjunct's name against the layer's own output, and
// the mapped target's recorded name against the child's output — a
// layer whose bookkeeping drifted declines instead of pushing a
// mislabeled qual (property 1 of the join pass, applied per layer).
func remapConjunctThroughProjection(c Expr, selfOut Schema, exprs []Expr, childOut Schema) (Expr, bool) {
	bad := false
	okWalk := walkExprRefs(c, scopeVeto, exprVisitor{
		Visit: func(e Expr) bool {
			if bad {
				return false
			}
			switch x := e.(type) {
			case *OuterColumnRef, *FuncCall:
				_ = x
				bad = true
				return false
			case *ColumnRef:
				if x.Index < 0 || x.Index >= len(exprs) {
					bad = true
					return false
				}
				if x.Index < len(selfOut) && x.Name != "" && !strings.EqualFold(selfOut[x.Index].Name, x.Name) {
					bad = true
					return false
				}
				t, isCol := exprs[x.Index].(*ColumnRef)
				if !isCol || t.Index < 0 || t.Index >= len(childOut) {
					bad = true
					return false
				}
				if t.Name != "" && !strings.EqualFold(childOut[t.Index].Name, t.Name) {
					bad = true
					return false
				}
			}
			return true
		},
	})
	if !okWalk || bad {
		return nil, false
	}
	return cloneExprRefs(c, scopeVeto, exprRewriter{
		Rewrite: func(e Expr) Expr {
			if cr, ok := e.(*ColumnRef); ok {
				t := exprs[cr.Index].(*ColumnRef)
				cr.Index = t.Index
				cr.Name = childOut[t.Index].Name
				cr.Type = t.Type
				cr.SourceTableIdx = t.SourceTableIdx
			}
			return e
		},
	})
}
