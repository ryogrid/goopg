package planner

import "strings"

// pushSingleSideQualsIntoInnerJoinInputs is the binary-join sibling of
// pushSingleSourceFiltersAfterRemap (mhj_input_rewrite.go) and the
// inner-join sibling of pushOuterQualsIntoLaterals (pushdown.go): when a
// residual `*Filter` sits directly above an INNER `*Join` and one of its
// conjuncts references columns from exactly ONE of the join's two inputs,
// a copy of that conjunct is attached to that input.
//
// This exists because goopg was evaluating a side-mixed join residual
// BEFORE the single-relation restrictions above the join had excluded the
// rows it could not cope with. TPC-DS Q75 is the witness: its final block
// self-joins the `all_sales` CTE with
//
//	AND curr_yr.d_year=2002 AND prev_yr.d_year=2002-1
//	AND CAST(curr_yr.sales_cnt AS DECIMAL(17,2))
//	    /CAST(prev_yr.sales_cnt AS DECIMAL(17,2))<0.9
//
// The CTE carries no year filter, so a `d_year = 2003` group with
// `sales_cnt = 0` reaches the hash join; goopg ran the division as the
// join residual on every matched pair and raised `division by zero`,
// while PostgreSQL never sees the zero because the two `d_year` quals are
// already applied at scan level.
//
// That is PG's architecture, not an accident of its evaluator. A qual
// referencing exactly one relation is a *restriction* clause:
// distribute_restrictinfo_to_rels
// (postgres/src/backend/optimizer/plan/initsplan.c) attaches it to that
// relation's baserestrictinfo, where it is applied by the scan; only
// clauses spanning two relations become join quals. Matching PG's qual
// PLACEMENT is therefore the fix — not a special case for division.
//
// Four load-bearing properties (docs/design/0125-0004-...md D1/D2):
//
//  1. It runs AFTER remapWithBindings, in the MHJ-output coordinate
//     space, and validates every ColumnRef POSITIONALLY BY NAME,
//     declining on any mismatch. This is RC-1b's whole lesson: the same
//     shape of pass running before the remap attributed conjuncts by
//     FROM-cumulative indices against OID-sorted offsets and pushed a
//     date_dim predicate onto `store` (TPC-DS Q47/Q50 wrong answers).
//     A path where the remap did not run degrades to post-join
//     evaluation — slower, never wrong.
//
//  2. It DUPLICATES rather than moves. The conjunct stays in the residual
//     Filter as well, so the transformation is idempotent on the result
//     SET (a filter applied twice selects the same rows) and only the
//     ERROR behaviour changes, intentionally, to match PG. It also
//     guarantees the chosen join ORDER cannot move as a side effect.
//
//  3. It places the Filter on the join's INPUT node, never inside a CTE
//     body. `all_sales` is referenced twice under different restrictions;
//     rewriting the body would apply one branch's filter to both. Wrapping
//     the *CTEScan — which is a labelling wrapper the executor unwraps —
//     leaves the shared body untouched.
//
//  4. INNER joins only. For an outer join, a restriction on the nullable
//     side changes which rows are null-extended. PG 18.3 no longer has the
//     old check_outerjoin_delay guard (removed in the nullingrels rework);
//     safety is now expressed through a clause's nulling-relids and
//     is_pushed_down, and goopg has no nullingrels model, so this pass
//     simply declines.
//
// Scoping (D2) is deliberately narrow: the target input must be a CTE
// reference, never a base-relation leaf. Pushing filters toward
// base-relation leaves is exactly what shouldAttachBeforeMHJ
// (local_filters.go) withholds behind its SmallDimension guard, whose
// comment records that without it "Slice A regresses Q8 / Q21 from PASS
// to CANCEL". Base-relation leaf filters keep going through
// attachRelationLocalFilters, whose plans are snapshot-pinned.
func pushSingleSideQualsIntoInnerJoinInputs(n Node) {
	if n == nil {
		return
	}
	switch x := n.(type) {
	case *Filter:
		pushInnerJoinInputQuals(x)
		pushSingleSideQualsIntoInnerJoinInputs(x.Child)
	case *Join:
		pushSingleSideQualsIntoInnerJoinInputs(x.Left)
		pushSingleSideQualsIntoInnerJoinInputs(x.Right)
	case *NestedLoopIndexJoin:
		pushSingleSideQualsIntoInnerJoinInputs(x.Outer)
		pushSingleSideQualsIntoInnerJoinInputs(x.Inner)
	case *MultiHashJoin:
		for _, t := range x.Tables {
			pushSingleSideQualsIntoInnerJoinInputs(t)
		}
	case *CTEScan:
		pushSingleSideQualsIntoInnerJoinInputs(x.Child)
	case *Project:
		pushSingleSideQualsIntoInnerJoinInputs(x.Child)
	case *Sort:
		pushSingleSideQualsIntoInnerJoinInputs(x.Child)
	case *Limit:
		pushSingleSideQualsIntoInnerJoinInputs(x.Child)
	case *Aggregate:
		pushSingleSideQualsIntoInnerJoinInputs(x.Child)
	case *WindowAgg:
		pushSingleSideQualsIntoInnerJoinInputs(x.Child)
	}
}

// pushInnerJoinInputQuals applies the transformation described on
// pushSingleSideQualsIntoInnerJoinInputs to one Filter-over-Join pair.
// It is a no-op unless f.Child is a non-lateral INNER Join.
func pushInnerJoinInputQuals(f *Filter) {
	if f == nil || f.Predicate == nil {
		return
	}
	j, ok := f.Child.(*Join)
	// Property 4: INNER only. A LATERAL inner join is also declined —
	// its right side is re-opened per outer row and is owned by
	// pushOuterQualsIntoLaterals, which has its own outer-side contract.
	if !ok || j.Type != JoinTypeInner || j.Lateral {
		return
	}
	// Wrapping an input in a Filter does not change its Output(), so
	// leftWidth is stable across the conjunct loop even as inputs are
	// rewritten.
	leftWidth := len(j.Left.Output())
	for _, c := range splitAnd(f.Predicate) {
		side, ok := innerJoinPushTarget(c, j, leftWidth)
		if !ok {
			continue
		}
		target := j.Left
		delta := 0
		if side == sideRight {
			target = j.Right
			delta = -leftWidth
		}
		// D2 scoping: a CTE reference or a base-relation leaf is a
		// legal target (see innerJoinPushEligibleInput for why the
		// leaf arm is safe here but not in Slice A). Unwrap one Filter
		// level so a second eligible conjunct ANDs into the wrapper the
		// first one created instead of stacking.
		inner := target
		if existing, isFilter := inner.(*Filter); isFilter {
			inner = existing.Child
		}
		if !innerJoinPushEligibleInput(inner) {
			continue
		}
		local, ok := shiftConjunctForInput(c, delta)
		if !ok {
			continue
		}
		if existing, isFilter := target.(*Filter); isFilter {
			// IDEMPOTENCE. A planned subtree is walked again when an
			// enclosing scope's planSelect reaches this pass with the
			// subtree already embedded, so without this guard the same
			// conjunct ANDs in once per enclosing scope — TPC-DS Q69
			// printed `d_year = 2002 AND d_moy >= 1 AND d_moy <= 3`
			// TWICE on each date_dim scan. Harmless for the result set
			// (a filter applied twice selects the same rows) but it
			// costs a re-evaluation per row and diverges from PG's
			// plan text. exprEqual is FAIL-CLOSED, so an undecidable
			// conjunct degrades to the old duplicate, never to a
			// dropped qual.
			dup := false
			for _, have := range splitAnd(existing.Predicate) {
				if exprEqual(have, local) {
					dup = true
					break
				}
			}
			if !dup {
				existing.Predicate = combineAnd([]Expr{existing.Predicate, local})
			}
			continue
		}
		// Property 2: the conjunct is DUPLICATED — f.Predicate is left
		// untouched — so only the error behaviour changes.
		//
		// LeafLocal follows the target: above a base-relation leaf the
		// shifted conjunct is in leaf coordinates (M0077-0001), which is
		// exactly what the flag asserts. A CTE reference keeps the
		// M0125-0004 behaviour (flag clear).
		wrapped := &Filter{
			pos:       c.Pos(),
			Child:     target,
			Predicate: local,
			LeafLocal: innerJoinPushLeafScan(inner),
		}
		if side == sideRight {
			j.Right = wrapped
		} else {
			j.Left = wrapped
		}
	}
}

// innerJoinPushEligibleInput reports whether a Join input may be wrapped
// in a pushed Filter. See D2 on pushSingleSideQualsIntoInnerJoinInputs:
// a base-relation leaf must NOT be a target, because it is a candidate
// member of a MultiHashJoin subset whose plans are snapshot-pinned and
// whose filter placement is governed by the SmallDimension gate.
func innerJoinPushEligibleInput(n Node) bool {
	switch n.(type) {
	case *CTEScan, *MaterializedCTEScan:
		return true
	}
	return innerJoinPushLeafScan(n)
}

// innerJoinPushLeafScan reports whether `n` is a base-relation leaf —
// a node whose Output() is exactly one relation's columns.
//
// D2 originally admitted CTE references ONLY, reasoning that "pushing
// filters toward base-relation leaves is exactly what
// shouldAttachBeforeMHJ withholds behind its SmallDimension guard".
// M0125-0035 retired that half of the scoping, because the two passes do
// not do the same thing:
//
//   - Slice A (shouldAttachBeforeMHJ → partitionConjunctsForJoinPlanning)
//     MOVES a conjunct out of the DP's input BEFORE enumeration, so it
//     changes the join ORDER as well as the qual placement. That is what
//     "Slice A regresses Q8 / Q21 from PASS to CANCEL" recorded, and why
//     its rollout stays gated and snapshot-pinned.
//   - This pass runs LAST (planner.go, after remapWithBindings and
//     pushSingleSourceFiltersAfterRemap) and DUPLICATES rather than moves
//     (property 2). The join order is already fixed when it runs, so
//     admitting a leaf changes what a scan EMITS, never which join is
//     built first.
//
// A Filter wrapping such a leaf carries LEAF-LOCAL ColumnRefs by the
// M0077-0001 convention — attachRelationLocalFilters sets the same flag
// on the same shape — and shiftConjunctForInput has already put the
// duplicated conjunct in the input's own coordinate space, so the two
// agree.
func innerJoinPushLeafScan(n Node) bool {
	switch n.(type) {
	case *SeqScan, *IndexScan, *IndexOnlyScan:
		return true
	}
	return false
}

// innerJoinPushTarget reports which single Join input covers every
// ColumnRef in c, validating each reference positionally by name against
// that input's output schema. The second result is false when the
// conjunct is not pushable at all — refs spanning both inputs, refs out
// of the join's width, a name that does not match the position it
// claims, an outer-scope reference, a sublink, a function call
// (see below), or an expression kind exprChildSlots does not enumerate.
//
// Function calls are declined because goopg's planner has no volatility
// model (there is no provolatile lookup in this package). Property 2
// duplicates the conjunct, so a volatile function would be evaluated
// once per input row AND once per matched pair; `random() < 0.5` applied
// twice does not select the same rows. Deferred with a ledger row —
// pushing an IMMUTABLE call is sound and is what PG does.
func innerJoinPushTarget(c Expr, j *Join, leftWidth int) (joinSide, bool) {
	leftOut := j.Left.Output()
	rightOut := j.Right.Output()
	totalWidth := leftWidth + len(rightOut)
	target := sideUnknown
	bad := false
	// scopeVeto aborts on any inner-plan child (SubqueryExpr,
	// ExistsExpr, MultiAssignSubq*), matching walkColumnRefsImpl's
	// onOuter contract; an unenumerated Expr kind aborts too, because
	// walkExprRefs is fail-closed. *OuterColumnRef is a childless leaf
	// in exprChildSlots, so it is NOT covered by the veto and must be
	// rejected here by hand.
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
				idx := x.Index
				if idx < 0 || idx >= totalWidth {
					bad = true
					return false
				}
				s := sideLeft
				out, local := leftOut, idx
				if idx >= leftWidth {
					s, out, local = sideRight, rightOut, idx-leftWidth
				}
				// Positional name validation (property 1).
				if x.Name != "" && !strings.EqualFold(out[local].Name, x.Name) {
					bad = true
					return false
				}
				if target == sideUnknown {
					target = s
				} else if target != s {
					bad = true
					return false
				}
			}
			return true
		},
	})
	if !okWalk || bad || target == sideUnknown {
		return sideUnknown, false
	}
	return target, true
}

// shiftConjunctForInput returns a deep copy of c with every
// ColumnRef.Index shifted by delta, so the copy reads in the target
// input's own coordinate space. The copy is essential: c stays in the
// residual Filter (property 2), and planner expression nodes are shared
// far more often than they look.
func shiftConjunctForInput(c Expr, delta int) (Expr, bool) {
	if delta == 0 {
		return cloneExprRefs(c, scopeVeto, exprRewriter{})
	}
	return cloneExprRefs(c, scopeVeto, exprRewriter{
		Rewrite: func(e Expr) Expr {
			if cr, ok := e.(*ColumnRef); ok {
				cr.Index += delta
			}
			return e
		},
	})
}
