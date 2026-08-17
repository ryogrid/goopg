package optimizer

import (
	"strings"

	"github.com/goopg/goopg/internal/parser"
)

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
//  4. PRESERVED sides only. For an outer join, a restriction on the
//     NULLABLE side changes which rows are null-extended, and PG 18.3
//     expresses that safety through a clause's nulling-relids and
//     is_pushed_down (the old check_outerjoin_delay guard went away in
//     the nullingrels rework). goopg has no nullingrels model, so the
//     nullable side is declined outright — but the PRESERVED side needs
//     no such model, because every preserved row reaches the output
//     either matched or null-extended. M0125-0035 arm (a) widened the
//     pass from "INNER only" to that rule; joinRestrictionSides is the
//     single place it is decided.
//
// Scoping (D2) started deliberately narrow — the target input had to be
// a CTE reference and had to be the join's IMMEDIATE child — and both
// halves have since been retired against evidence:
//
//   - base-relation leaves became legal targets in M0125-0035's binary
//     arm (see innerJoinPushLeafScan for why the SmallDimension risk it
//     borrowed from Slice A does not transfer to a pass that runs last
//     and duplicates);
//   - the immediate-child restriction went in arm (a): a restriction is
//     now carried down the join spine to the deepest node that can hold
//     it (pushConjunctIntoSubtree), which is where PG files it.
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
// It is a no-op unless f.Child is a non-lateral Join of a pushable
// type (see joinRestrictionSides).
//
// The Filter-over-Join ENTRY shape is deliberate even though
// pushConjunctIntoSubtree can attach below any node: a Filter sitting
// directly on a CTE reference or a leaf already applies the conjunct
// exactly where PG would, so descending there would only stack a
// second, redundant Filter and churn the plan text.
func pushInnerJoinInputQuals(f *Filter) {
	if f == nil || f.Predicate == nil {
		return
	}
	switch child := f.Child.(type) {
	case *Join:
		if _, _, pushable := joinRestrictionSides(child); !pushable {
			return
		}
		for _, c := range splitAnd(f.Predicate) {
			// Property 2: the conjunct is DUPLICATED — f.Predicate is left
			// untouched — so only the error behaviour changes.
			if repl, ok := pushConjunctIntoSubtree(child, c); ok {
				f.Child = repl
				// …only the error behaviour, and (until M0127-P5.6-f-vi)
				// the estimate: the copy below is priced by the node it
				// was attached to AND the original is priced here. Record
				// the duplication so `filterSelectivity` charges it once.
				f.notePushedBelow(c)
			}
		}
	}
}

// notePushedBelow records that conjunct `c` of f.Predicate has been
// duplicated onto a descendant, so the descendant's own estimate already
// charges it. Idempotent: both pushdown passes are re-walk safe
// (pushConjunctIntoSubtree's exprEqual guard), so a second walk must not
// grow the list.
func (f *Filter) notePushedBelow(c Expr) {
	for _, seen := range f.PushedBelow {
		if seen == c || exprEqual(seen, c) {
			return
		}
	}
	f.PushedBelow = append(f.PushedBelow, c)
}

// pricedBelow reports whether conjunct c is one this Filter has already
// charged at a descendant. Matched by `exprEqual` rather than pointer
// identity because a later rewriter may clone the predicate wholesale
// (cloneExprForShift and the posMap passes both do); identity alone would
// silently go stale and restore the double-charge.
func (f *Filter) pricedBelow(c Expr) bool {
	for _, seen := range f.PushedBelow {
		if seen == c || exprEqual(seen, c) {
			return true
		}
	}
	return false
}

// `pushResidualQualsIntoMHJTables` and `mhjResidualConjunctTable` were the
// `*MultiHashJoin` arm of `pushInnerJoinInputQuals` (M0125-0046), deleted with
// the node by M0127-P6.2. They handled the conjunct that fell between two
// stools when packing replaced a join chain: `pushOneConjunct` had never AND'd
// it onto a join predicate, so the packer's `extras` capture (join-predicate
// conjuncts only) missed it and it stayed above the packed node, leaving TPC-DS
// Q47's `ca_state IN ('IL','TX','ME')` un-pushed and customer_address scanned
// whole. The binary `*Join` arm above is the surviving path and keeps both
// load-bearing properties: the conjunct is DUPLICATED, not moved, and it is
// attributed to a unique input by validating each ColumnRef positionally
// against that input's schema by name.

// joinRestrictionSides reports which of a join's inputs may receive a
// copy of a restriction clause that sits ABOVE the join, and whether
// this join kind participates at all.
//
// This is property 4 of pushSingleSideQualsIntoInnerJoinInputs, refined
// by M0125-0035 arm (a) from "INNER only" to "every side PG would call
// preserved":
//
//   - INNER / CROSS — both inputs. A CROSS join is an inner join whose
//     predicate is absent (or was demoted to a residual Filter, which is
//     precisely the M0125-0034 C1 shape), so a single-side restriction is
//     as safe there as on any inner join, and it is worth far more: it
//     shrinks an input of a Cartesian product.
//   - LEFT — the LEFT (preserved) input only. Every preserved-side row
//     reaches the join output at least once, either matched or
//     null-extended, and the restriction does not mention the nullable
//     side, so removing a preserved row before the join removes exactly
//     the output rows the Filter above would have removed. This needs no
//     nullingrels model, which is why it is safe here while the nullable
//     side is not: a restriction on the NULLABLE side would delete rows
//     that the join would otherwise have null-extended and kept. (PG
//     reaches the same place from the other direction — a strict WHERE
//     qual on the nullable side lets reduce_outer_joins turn the join
//     into an inner join first; goopg has no strictness model either, so
//     it declines.)
//   - RIGHT — mirror image: the RIGHT input only.
//   - FULL — neither input is preserved.
//   - SEMI / ANTI — declined for a second, independent reason: their
//     Output() is Left's layout alone (see Join.Output), so the
//     leftWidth/totalWidth coordinate arithmetic below does not describe
//     the space the Filter's ColumnRefs live in.
//
// A LATERAL join is declined whatever its type: its right side is
// re-opened per outer row and is owned by pushOuterQualsIntoLaterals,
// which has its own outer-side contract.
func joinRestrictionSides(j *Join) (left, right, pushable bool) {
	if j == nil || j.Lateral {
		return false, false, false
	}
	switch j.Type {
	case JoinTypeInner, JoinTypeCross:
		return true, true, true
	case JoinTypeLeft:
		return true, false, true
	case JoinTypeRight:
		return false, true, true
	}
	return false, false, false
}

// pushConjunctIntoSubtree attaches a copy of conjunct c — expressed in
// n's OWN coordinate space — to the deepest node of n's subtree that a
// PG restriction clause would reach, and returns the replacement for n.
//
// PG's analogue is distribute_restrictinfo_to_rels
// (postgres/src/backend/optimizer/plan/initsplan.c), which files a
// single-relation restriction directly on that relation's
// baserestrictinfo no matter how deep the join tree above it is. The
// original M0125-0004 pass stopped at the join's immediate input, so a
// restriction only ever reached a leaf that happened to be a direct
// child of the join carrying the residual Filter. Descending is what
// lets a conjunct cross the join spine that TPC-DS builds above a
// dimension table.
//
// The recursion is coordinate-correct by construction: a Filter does not
// change its child's Output(), and shiftConjunctForInput re-bases the
// copy into the chosen input's space before the next level sees it.
func pushConjunctIntoSubtree(n Node, c Expr) (Node, bool) {
	switch x := n.(type) {
	case *Filter:
		// Descend past a Filter only when its child is a Join. If the
		// child is a terminal target, ANDing into THIS Filter is both
		// the shorter plan and the idempotence guard below; recursing
		// would wrap the leaf a second time on every re-walk.
		if _, isJoin := x.Child.(*Join); isJoin {
			repl, ok := pushConjunctIntoSubtree(x.Child, c)
			if ok {
				x.Child = repl
			}
			return x, ok
		}
		if !innerJoinPushEligibleInput(x.Child) {
			return n, false
		}
		// A Filter's LeafLocal describes the coordinate convention of
		// its whole predicate, so a conjunct may only join one whose
		// convention already matches what this pass would have set.
		// Fail-closed: a mismatch declines instead of mixing two
		// coordinate spaces inside one AND-chain.
		if x.LeafLocal != innerJoinPushLeafScan(x.Child) {
			return n, false
		}
		// IDEMPOTENCE. A planned subtree is walked again when an
		// enclosing scope's planSelect reaches this pass with the
		// subtree already embedded, so without this guard the same
		// conjunct ANDs in once per enclosing scope — TPC-DS Q69
		// printed `d_year = 2002 AND d_moy >= 1 AND d_moy <= 3` TWICE
		// on each date_dim scan. Harmless for the result set (a filter
		// applied twice selects the same rows) but it costs a
		// re-evaluation per row and diverges from PG's plan text.
		// exprEqual is FAIL-CLOSED, so an undecidable conjunct degrades
		// to the old duplicate, never to a dropped qual.
		for _, have := range splitAnd(x.Predicate) {
			if exprEqual(have, c) {
				return n, true
			}
		}
		x.Predicate = combineAnd([]Expr{x.Predicate, c})
		return x, true

	case *Join:
		leftOK, rightOK, pushable := joinRestrictionSides(x)
		if !pushable {
			return n, false
		}
		leftWidth := len(x.Left.Output())
		side, ok := innerJoinPushTarget(c, x, leftWidth)
		if !ok {
			return n, false
		}
		target, delta := x.Left, 0
		if side == sideRight {
			if !rightOK {
				return n, false
			}
			target, delta = x.Right, -leftWidth
		} else if !leftOK {
			return n, false
		}
		// M0125-0035 EC arm: while c descends into its own side, a
		// `col = const` conjunct also seeds the OTHER side through this
		// join's own equality clauses (see deriveConstAcrossJoinEquality
		// for why that is safe even on a nullable side this function
		// would otherwise refuse to touch).
		deriveConstAcrossJoinEquality(x, c, side, leftWidth)
		local, ok := shiftConjunctForInput(c, delta)
		if !ok {
			return n, false
		}
		repl, ok := pushConjunctIntoSubtree(target, local)
		if !ok {
			return n, false
		}
		if side == sideRight {
			x.Right = repl
		} else {
			x.Left = repl
		}
		return x, true
	}

	// D2 scoping: a CTE reference or a base-relation leaf is a legal
	// terminal target (see innerJoinPushEligibleInput for why the leaf
	// arm is safe here but not in Slice A).
	if !innerJoinPushEligibleInput(n) {
		return n, false
	}
	// LeafLocal follows the target: above a base-relation leaf the
	// shifted conjunct is in leaf coordinates (M0077-0001), which is
	// exactly what the flag asserts. A CTE reference keeps the
	// M0125-0004 behaviour (flag clear).
	return &Filter{
		pos:       c.Pos(),
		Child:     n,
		Predicate: c,
		LeafLocal: innerJoinPushLeafScan(n),
	}, true
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
// shouldAttachLocalFiltersBeforeSearch withholds behind its SmallDimension guard".
// M0125-0035 retired that half of the scoping, because the two passes do
// not do the same thing:
//
//   - Slice A (shouldAttachLocalFiltersBeforeSearch → partitionConjunctsForJoinPlanning)
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

// deriveConstAcrossJoinEquality is goopg's bounded analogue of PG's
// equivalence-class constant propagation (postgres/src/backend/
// optimizer/path/equivclass.c, generate_implied_equalities_for_column):
// when a `col = const` restriction descends through a join whose own
// predicate contains an equality `col = col'` linking it to a column of
// the OTHER input, the matched pairs of this join provably satisfy
// `col' = const` too, so a derived copy is seeded into that other input.
//
// TPC-DS Q78 is the witness and the reason the CTE-body pass alone
// could not meet the item's acceptance: `ss_sold_year = 1998` descends
// the preserved spine to the `ss` reference (and from there into the
// body, down to `date_dim`), but the `ws` and `cs` channels hang off
// the NULLABLE sides of two `LEFT JOIN ... ON ws_sold_year =
// ss_sold_year AND ...`, which joinRestrictionSides rightly refuses for
// the ORIGINAL conjunct. PG still filters all three channels by
// `d_year = 1998`, and this derivation is how: `ws_sold_year = 1998`
// holds for every ws row that can MATCH (matching requires
// ws_sold_year = ss_sold_year, and c holds on the preserved side), so
// pre-filtering the nullable input removes only rows that were headed
// for no-match — the preserved rows they would not have matched are
// null-extended exactly as before. This is the one shape where seeding
// a nullable side needs no nullingrels model.
//
// Soundness at depth: c may have descended several joins below the
// residual Filter that owns it. Property 2 (duplicate-never-move)
// keeps the original conjunct in that residual, so any pair this join
// emits whose preserved half violates c is discarded above regardless
// of whether the derived filter changed a match into a null-extension
// on the way.
//
// Fail-closed bounds: the conjunct must be a bare `ColumnRef = const`
// (isConstantPlanExpr excludes ColumnRefs, sublinks and FuncCalls, so
// no volatility or scope question arises); the join equality must be
// between two bare ColumnRefs validated positionally by name (property
// 1); and the two columns must agree on type name — goopg has no
// opfamily model, and cross-type equality transitivity is exactly what
// PG makes the opfamily prove.
func deriveConstAcrossJoinEquality(j *Join, c Expr, side joinSide, leftWidth int) {
	if j.Predicate == nil {
		return
	}
	eq, ok := c.(*BinaryOp)
	if !ok || eq.Op != parser.OpEq {
		return
	}
	colRef, okc := eq.Left.(*ColumnRef)
	constOperand := eq.Right
	if !okc {
		colRef, okc = eq.Right.(*ColumnRef)
		constOperand = eq.Left
	}
	if !okc || !isConstantPlanExpr(constOperand) {
		return
	}
	leftOut, rightOut := j.Left.Output(), j.Right.Output()
	validRef := func(cr *ColumnRef) bool {
		out, local := leftOut, cr.Index
		if cr.Index >= leftWidth {
			out, local = rightOut, cr.Index-leftWidth
		}
		if local < 0 || local >= len(out) {
			return false
		}
		return cr.Name == "" || strings.EqualFold(out[local].Name, cr.Name)
	}
	for _, jc := range splitAnd(j.Predicate) {
		peq, ok := jc.(*BinaryOp)
		if !ok || peq.Op != parser.OpEq {
			continue
		}
		a, aok := peq.Left.(*ColumnRef)
		b, bok := peq.Right.(*ColumnRef)
		if !aok || !bok {
			continue
		}
		var match, other *ColumnRef
		switch colRef.Index {
		case a.Index:
			match, other = a, b
		case b.Index:
			match, other = b, a
		default:
			continue
		}
		matchOnLeft := match.Index < leftWidth
		otherOnLeft := other.Index < leftWidth
		// The pair must span the two inputs, with the matched column on
		// c's own side.
		if matchOnLeft == otherOnLeft || (side == sideLeft) != matchOnLeft {
			continue
		}
		if !validRef(match) || !validRef(other) {
			continue
		}
		if !strings.EqualFold(match.Type.Name, other.Type.Name) {
			continue
		}
		// Re-point c's single ColumnRef at the other side's column; the
		// clone keeps the comparison node (and its ResultType) intact.
		// The name is taken from the SCHEMA position, not the predicate's
		// ref, so the deeper descent's positional-name validation checks
		// something real even when the predicate ref carried no name.
		var otherName string
		if otherOnLeft {
			otherName = leftOut[other.Index].Name
		} else {
			otherName = rightOut[other.Index-leftWidth].Name
		}
		derived, ok := cloneExprRefs(c, scopeVeto, exprRewriter{
			Rewrite: func(e Expr) Expr {
				if cr, isCR := e.(*ColumnRef); isCR {
					cr.Index = other.Index
					cr.Name = otherName
					cr.Type = other.Type
					cr.SourceTableIdx = other.SourceTableIdx
				}
				return e
			},
		})
		if !ok {
			continue
		}
		target, delta := j.Left, 0
		if !otherOnLeft {
			target, delta = j.Right, -leftWidth
		}
		local, ok := shiftConjunctForInput(derived, delta)
		if !ok {
			continue
		}
		if repl, ok := pushConjunctIntoSubtree(target, local); ok {
			if otherOnLeft {
				j.Left = repl
			} else {
				j.Right = repl
			}
		}
	}
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
