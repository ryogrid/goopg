package planner

import "github.com/goopg/goopg/internal/catalog"

// exprwalk.go — the single child-slot primitive every planner expression
// traversal is meant to be built on, plus three deliberately
// distinctly-typed drivers over it.
//
// M0125-0001 (docs/design/0125-0001-exprwalk-driver-and-exhaustiveness-gate.md,
// from docs/design/tpcds-round2-fixes/README.md §13.5 action 4, phase 1.1).
//
// WHY THIS EXISTS
//
// The planner grew ~10 hand-written type switches over Expr (bushy.go's
// visitColumnRefs / visitColumnRefsForTable / visitColumnRefsByName,
// pushdown.go's walkColumnRefsImpl, local_filters.go's
// conjunctIsLocalEligible / localizeExprToLeaf, nl_index_join.go's
// cloneExprShiftIdx, planner.go's exprSide, bushy.go's remapByPosMap).
// Each enumerates its own subset of the 32 concrete Expr types, and an
// UNENUMERATED type is a silent no-op rather than an error. That is the
// RC-1a defect class: for TPC-DS Q76, `WHERE ss_customer_sk IS NULL` kept
// its pre-rewrite index because remapByPosMap had no *IsNullExpr arm, so
// IS NULL was evaluated against a date_dim column that is never NULL and
// the query returned 0 rows instead of 100 (§2 of the round-2 doc).
//
// The fix is not "add the missing arms" — that is what produced the
// current state. It is to have ONE place that knows the child structure
// of an Expr, and a build-time gate that fails when a 33rd type is added
// without teaching it. See exprwalk_exhaustive_test.go: it compares
// plan.go's `exprNode()` receivers against this file's type switches in
// BOTH directions, so both a missing arm and a stale arm are test
// failures.
//
// NO CALL SITE IS CONVERTED IN THIS COMMIT. Converting the seven
// remaining walkers is M0125-0002, and it is a plan-SHAPE change (see
// that task: extraInScans admits unenumerated conjuncts into
// MultiHashJoin.Filters *by accident*, so completing a walker REMOVES
// predicates). This file is inert until a caller opts in.

// exprSlotKind classifies a child slot by its COORDINATE SPACE, which is
// the property callers actually branch on. Classify by kind — never by
// len(slots): a scope-opening node such as *ArraySubqueryExpr reports
// ZERO same-scope slots and is still very much not a leaf.
type exprSlotKind uint8

const (
	// slotSameScope is an *Expr child evaluated in the SAME row
	// coordinate space as its parent. Column indices inside it are
	// directly comparable with the parent's, so a positional remap
	// must descend here.
	slotSameScope exprSlotKind = iota

	// slotInnerPlan is a Node child evaluated in a DIFFERENT coordinate
	// space (a correlated subquery's own plan tree). Indices inside it
	// index the inner plan's schema, NOT the parent's — remapping it
	// positionally corrupts the plan. The one thing that DOES connect
	// the spaces is an OuterColumnRef whose Level names the parent
	// scope; translating those is remapOuterRefsInSubplan's job, which
	// is why scopeDescend hands the Node to the caller rather than
	// walking it here.
	//
	// A Semi/Anti join's inner plan is the canonical "must NOT be
	// remapped" case.
	slotInnerPlan

	// slotSubqRow is *MultiAssignSubqElem.Row. It is statically typed
	// *MultiAssignSubqRow, NOT Expr, so a type switch over Expr can
	// never reach it as a child even though *MultiAssignSubqRow does
	// implement Expr. It is also SHARED: every MultiAssignSubqElem of
	// one `SET (a,b) = (SELECT x,y …)` assignment points at the same
	// MultiAssignSubqRow, and the executor caches the subquery result
	// keyed by that pointer (Context.MultiAssignSubqCache). Cloning or
	// replacing it per-element would break both the sharing and the
	// cache key, so the drivers here treat it as an inner scope and
	// never rewrite through it.
	slotSubqRow
)

// exprSlot is a POINTER to a child slot, so the same primitive serves
// read-only walks and in-place rewrites. Exactly one of the three
// pointers is non-nil, selected by kind.
type exprSlot struct {
	kind exprSlotKind
	expr *Expr                // kind == slotSameScope
	plan *Node                // kind == slotInnerPlan
	row  **MultiAssignSubqRow // kind == slotSubqRow
}

// exprChildSlots returns the child slots of e, and whether e's concrete
// type is enumerated here.
//
// ok == false means "this type is not known to the planner's traversal
// layer". Every driver below treats that FAIL-CLOSED (abort / veto)
// rather than fail-open, which is the inverse of the historical
// behaviour: walkColumnRefsImpl's missing `default:` means an
// unenumerated node yields no onOuter() veto, so a conjunct wrapping an
// outer reference reads as single-side and can be pushed below an outer
// join (recorded during M0124-0003's re-verification). Silence must mean
// "refuse", not "safe".
//
// The slots are returned in evaluation order where that is observable.
func exprChildSlots(e Expr) ([]exprSlot, bool) {
	switch x := e.(type) {

	// ---- childless leaves (14) -------------------------------------
	// These carry no Expr, Node or MultiAssignSubqRow field at all, so
	// every traversal terminates here. They are listed explicitly, not
	// swept into `default:`, precisely so the exhaustiveness test can
	// tell "known to have no children" from "never taught".
	case *IntegerConst, *StringConst, *NumericConst, *TypedStringLit,
		*IntervalLit, *NullConst, *BooleanConst,
		*ColumnRef, *OuterColumnRef, *ParamRef, *ExecParamRef,
		*TableOidExpr, *CTIDExpr, *MergeActionExpr:
		return nil, true

	// MergeWholeRowRef is a leaf too, but kept on its own line because
	// it reads as a row-valued node and has been mistaken for a
	// container: the composite it yields is materialised by the
	// executor from ctx, not from child expressions.
	case *MergeWholeRowRef:
		return nil, true

	// ---- single same-scope operand ---------------------------------
	case *ExtractExpr:
		return []exprSlot{{kind: slotSameScope, expr: &x.Source}}, true
	case *IsNullExpr:
		return []exprSlot{{kind: slotSameScope, expr: &x.Operand}}, true
	case *IsBoolExpr:
		return []exprSlot{{kind: slotSameScope, expr: &x.Operand}}, true
	case *CollateExpr:
		return []exprSlot{{kind: slotSameScope, expr: &x.Operand}}, true
	case *CastExpr:
		return []exprSlot{{kind: slotSameScope, expr: &x.Operand}}, true
	case *UnaryOp:
		return []exprSlot{{kind: slotSameScope, expr: &x.Operand}}, true

	// ---- two same-scope operands -----------------------------------
	case *BinaryOp:
		return []exprSlot{
			{kind: slotSameScope, expr: &x.Left},
			{kind: slotSameScope, expr: &x.Right},
		}, true
	case *IsDistinctFromExpr:
		return []exprSlot{
			{kind: slotSameScope, expr: &x.Left},
			{kind: slotSameScope, expr: &x.Right},
		}, true

	// ---- same-scope lists ------------------------------------------
	case *FuncCall:
		slots := make([]exprSlot, 0, len(x.Args))
		for i := range x.Args {
			slots = append(slots, exprSlot{kind: slotSameScope, expr: &x.Args[i]})
		}
		return slots, true
	case *RowExpr:
		slots := make([]exprSlot, 0, len(x.Elems))
		for i := range x.Elems {
			slots = append(slots, exprSlot{kind: slotSameScope, expr: &x.Elems[i]})
		}
		return slots, true

	// CaseExpr's Whens is []CaseWhen — a STRUCT slice, not []Expr — so
	// the slots must point at the fields inside each element
	// (&x.Whens[i].When). Ranging by value and taking &w.When would
	// address the loop copy and drop every rewrite silently.
	case *CaseExpr:
		slots := make([]exprSlot, 0, 2+2*len(x.Whens))
		if x.Operand != nil { // nil for the searched form
			slots = append(slots, exprSlot{kind: slotSameScope, expr: &x.Operand})
		}
		for i := range x.Whens {
			slots = append(slots,
				exprSlot{kind: slotSameScope, expr: &x.Whens[i].When},
				exprSlot{kind: slotSameScope, expr: &x.Whens[i].Then})
		}
		if x.Else != nil {
			slots = append(slots, exprSlot{kind: slotSameScope, expr: &x.Else})
		}
		return slots, true

	// ---- scope-opening nodes ---------------------------------------
	// Args are the PARAM_EXEC-style argument expressions of D4.1's
	// subplan lowering: they are evaluated against the CURRENT OUTER
	// ROW before Plan runs, so despite belonging to a subquery node
	// they live in the PARENT's coordinate space and are same-scope
	// slots. Missing that is what remapByPosMap's InExpr comment
	// records as "previously skipped".
	case *InExpr:
		slots := make([]exprSlot, 0, 2+len(x.List)+len(x.Args))
		if x.Operand != nil {
			slots = append(slots, exprSlot{kind: slotSameScope, expr: &x.Operand})
		}
		for i := range x.List {
			slots = append(slots, exprSlot{kind: slotSameScope, expr: &x.List[i]})
		}
		for i := range x.Args {
			slots = append(slots, exprSlot{kind: slotSameScope, expr: &x.Args[i]})
		}
		// Either Plan or List is populated, never both.
		if x.Plan != nil {
			slots = append(slots, exprSlot{kind: slotInnerPlan, plan: &x.Plan})
		}
		return slots, true

	case *ExistsExpr:
		slots := make([]exprSlot, 0, 1+len(x.Args))
		for i := range x.Args {
			slots = append(slots, exprSlot{kind: slotSameScope, expr: &x.Args[i]})
		}
		if x.Plan != nil {
			slots = append(slots, exprSlot{kind: slotInnerPlan, plan: &x.Plan})
		}
		return slots, true

	case *SubqueryExpr:
		slots := make([]exprSlot, 0, 1+len(x.Args))
		for i := range x.Args {
			slots = append(slots, exprSlot{kind: slotSameScope, expr: &x.Args[i]})
		}
		if x.Plan != nil {
			slots = append(slots, exprSlot{kind: slotInnerPlan, plan: &x.Plan})
		}
		return slots, true

	// ArraySubqueryExpr and MultiAssignSubqRow have NO same-scope slots
	// at all (no Args — they were never lowered to PARAM_EXEC). They
	// are the reason callers must branch on slot kind rather than on
	// len(slots): both would read as leaves under a count test.
	case *ArraySubqueryExpr:
		if x.Plan == nil {
			return nil, true
		}
		return []exprSlot{{kind: slotInnerPlan, plan: &x.Plan}}, true

	case *MultiAssignSubqRow:
		if x.Plan == nil {
			return nil, true
		}
		return []exprSlot{{kind: slotInnerPlan, plan: &x.Plan}}, true

	// MultiAssignSubqElem reaches its inner plan only THROUGH Row,
	// which is statically *MultiAssignSubqRow. See slotSubqRow.
	case *MultiAssignSubqElem:
		if x.Row == nil {
			return nil, true
		}
		return []exprSlot{{kind: slotSubqRow, row: &x.Row}}, true
	}

	return nil, false
}

// scopePolicy selects what a driver does when it meets a slotInnerPlan
// (or slotSubqRow) child. These four are not hypothetical: each is a
// behaviour some existing walker relies on.
type scopePolicy uint8

const (
	// scopeIgnore steps over inner plans silently. remapByPosMap's
	// InExpr arm does this ("do NOT remap the inner Plan (already
	// remapped)").
	scopeIgnore scopePolicy = iota

	// scopeSignal reports the crossing via OnScope but does not
	// descend. This is the shape a caller wants when the subquery is
	// merely evidence about the enclosing expression.
	scopeSignal

	// scopeVeto reports via OnScope and ABORTS the traversal with a
	// false result. walkColumnRefsImpl's `onOuter()` on
	// SubqueryExpr/ExistsExpr/MultiAssignSubq* is this policy — the
	// conjunct is disqualified from pushdown outright.
	scopeVeto

	// scopeDescend hands the Node to OnScope and expects the CALLER to
	// traverse it. Node traversal is deliberately out of scope for this
	// file: it is a second exhaustiveness problem over the Node type
	// set, and the real call site (remapByPosMap → remapOuterRefsInSubplan)
	// already owns a traversal that knows about scope DEPTH, which a
	// generic walker here could not supply. Recorded in
	// .ralph/deferral_ledger.md as `tpcds-round2 exprwalk-node-side`.
	scopeDescend
)

// exprVisitor is the read-only driver's callback set.
type exprVisitor struct {
	// Visit is called for each Expr node PRE-order. Returning false
	// prunes that node's children without aborting the walk.
	Visit func(Expr) bool
	// OnScope is called for each inner-scope child under scopeSignal,
	// scopeVeto or scopeDescend. May be nil.
	OnScope func(Node)
	// OnUnknown is called when exprChildSlots does not recognise a
	// type. The walk then aborts (fail-closed). May be nil.
	OnUnknown func(Expr)
}

// walkExprRefs traverses e read-only under pol.
//
// It returns false when the walk was ABORTED — either by scopeVeto or by
// an unenumerated type. It does NOT return false merely because Visit
// pruned a subtree. Callers that mean "is this conjunct eligible" should
// read the false result as "not eligible".
//
// Distinct signature from the two rewriters below on purpose: passing a
// rewrite intent here cannot compile, so the historical failure mode of
// conflating a walk with a rewrite (and silently dropping the rewrite)
// is a build error.
func walkExprRefs(e Expr, pol scopePolicy, v exprVisitor) bool {
	if e == nil {
		return true
	}
	if v.Visit != nil && !v.Visit(e) {
		return true // pruned, not aborted
	}
	slots, ok := exprChildSlots(e)
	if !ok {
		if v.OnUnknown != nil {
			v.OnUnknown(e)
		}
		return false
	}
	for _, s := range slots {
		switch s.kind {
		case slotSameScope:
			if !walkExprRefs(*s.expr, pol, v) {
				return false
			}
		case slotInnerPlan:
			if !scopeVisit(*s.plan, pol, v.OnScope) {
				return false
			}
		case slotSubqRow:
			row := *s.row
			if row == nil {
				continue
			}
			if !scopeVisit(row.Plan, pol, v.OnScope) {
				return false
			}
		}
	}
	return true
}

// scopeVisit applies pol to one inner-scope child. Returns false only
// under scopeVeto.
func scopeVisit(n Node, pol scopePolicy, onScope func(Node)) bool {
	switch pol {
	case scopeIgnore:
		return true
	case scopeSignal, scopeDescend:
		if onScope != nil {
			onScope(n)
		}
		return true
	case scopeVeto:
		if onScope != nil {
			onScope(n)
		}
		return false
	}
	return true
}

// exprRewriter is the callback set shared by the two rewriting drivers.
type exprRewriter struct {
	// Rewrite maps a node to its replacement and is called BOTTOM-UP:
	// when it runs, the node's children have already been rewritten.
	// Returning the argument unchanged is a no-op. May be nil.
	Rewrite func(Expr) Expr
	// OnScope is called for each inner-scope child under scopeSignal,
	// scopeVeto or scopeDescend. Under scopeDescend this is where a
	// caller runs its own Node traversal (e.g. remapOuterRefsInSubplan).
	OnScope func(Node)
	// OnUnknown is called when exprChildSlots does not recognise a
	// type; the rewrite then aborts. May be nil.
	OnUnknown func(Expr)
}

// rewriteExprRefsInPlace rewrites the tree rooted at *e IN PLACE, mutating
// the existing nodes.
//
// Use this only when the tree is uniquely owned. Planner expression
// nodes are shared far more often than they look — remapByPosMap's
// ColumnRef arm copies the node before changing Index for exactly that
// reason — so cloneExprRefs is the safer default.
//
// Takes **Expr** (not Expr) so the root itself is replaceable; that also
// makes it impossible to call by accident where walkExprRefs was
// meant.
//
// A note on the name: this package already contains walkExpr (over
// parser.Expr), walkExprTree, walkExprTreeDeep, walkPlanExprs and
// walkPlanExprsDeep. The `BySlots` suffix is not decoration — it is the
// only thing distinguishing this family from five near-homonyms, three
// of which take a bare callback and would compile if confused for one
// another at a call site.
func rewriteExprRefsInPlace(e *Expr, pol scopePolicy, r exprRewriter) bool {
	if e == nil || *e == nil {
		return true
	}
	slots, ok := exprChildSlots(*e)
	if !ok {
		if r.OnUnknown != nil {
			r.OnUnknown(*e)
		}
		return false
	}
	for _, s := range slots {
		switch s.kind {
		case slotSameScope:
			if !rewriteExprRefsInPlace(s.expr, pol, r) {
				return false
			}
		case slotInnerPlan:
			if !scopeVisit(*s.plan, pol, r.OnScope) {
				return false
			}
		case slotSubqRow:
			row := *s.row
			if row == nil {
				continue
			}
			// Never rewritten through: shared across every elem of
			// the assignment and used as an executor cache key.
			if !scopeVisit(row.Plan, pol, r.OnScope) {
				return false
			}
		}
	}
	if r.Rewrite != nil {
		*e = r.Rewrite(*e)
	}
	return true
}

// cloneExprRefs returns a NEW tree, leaving e untouched.
//
// Returns (nil, false) when the rewrite was aborted (scopeVeto or an
// unenumerated type). Callers must check the bool — cloneExprShiftIdx,
// the existing driver of this shape, is a fail-closed admission test
// whose false result means "this conjunct cannot be shifted".
//
// Two semantic notes for whoever converts a call site:
//
//   - Every node is shallow-cloned, INCLUDING leaves. cloneExprShiftIdx
//     shares literal nodes ("literals are immutable — share"); this does
//     not. Sharing is unsafe in general here because the rewriter may
//     legitimately want to replace a leaf — indeed ColumnRef, the whole
//     point of a positional shift, IS a leaf. The cost is a few extra
//     allocations per conjunct on a plan-time path.
//   - Inner plans are NOT cloned. The clone's Plan field aliases the
//     original's Node, matching every existing driver: no walker in this
//     package deep-copies a subplan.
func cloneExprRefs(e Expr, pol scopePolicy, r exprRewriter) (Expr, bool) {
	if e == nil {
		return nil, true
	}
	c, ok := shallowCloneExpr(e)
	if !ok {
		if r.OnUnknown != nil {
			r.OnUnknown(e)
		}
		return nil, false
	}
	// Slots are taken from the CLONE, so writing through them cannot
	// touch the original. shallowCloneExpr copies every slice for the
	// same reason: a plain struct copy would alias the backing array
	// and a rewrite of x.Args[0] would be visible through the original.
	slots, ok := exprChildSlots(c)
	if !ok {
		if r.OnUnknown != nil {
			r.OnUnknown(c)
		}
		return nil, false
	}
	for _, s := range slots {
		switch s.kind {
		case slotSameScope:
			nv, ok := cloneExprRefs(*s.expr, pol, r)
			if !ok {
				return nil, false
			}
			*s.expr = nv
		case slotInnerPlan:
			if !scopeVisit(*s.plan, pol, r.OnScope) {
				return nil, false
			}
		case slotSubqRow:
			row := *s.row
			if row == nil {
				continue
			}
			if !scopeVisit(row.Plan, pol, r.OnScope) {
				return nil, false
			}
		}
	}
	if r.Rewrite != nil {
		c = r.Rewrite(c)
	}
	return c, true
}

// shallowCloneExpr copies one node, duplicating any slice field so the
// copy's child slots are independent of the original's. It does NOT
// recurse: the returned node's Expr children still point at the
// original's children until cloneExprRefs overwrites them.
//
// ok == false for an unenumerated type, same fail-closed contract as
// exprChildSlots. The exhaustiveness test asserts this switch covers
// exactly the same 32 types.
func shallowCloneExpr(e Expr) (Expr, bool) {
	switch x := e.(type) {

	// ---- no slice fields: a plain struct copy suffices -------------
	case *IntegerConst:
		c := *x
		return &c, true
	case *StringConst:
		c := *x
		return &c, true
	case *NumericConst:
		c := *x
		return &c, true
	case *TypedStringLit:
		c := *x
		return &c, true
	case *IntervalLit:
		c := *x
		return &c, true
	case *NullConst:
		c := *x
		return &c, true
	case *BooleanConst:
		c := *x
		return &c, true
	case *ColumnRef:
		c := *x
		return &c, true
	case *OuterColumnRef:
		c := *x
		return &c, true
	case *ParamRef:
		c := *x
		return &c, true
	case *ExecParamRef:
		c := *x
		return &c, true
	case *TableOidExpr:
		c := *x
		return &c, true
	case *CTIDExpr:
		c := *x
		return &c, true
	case *MergeActionExpr:
		c := *x
		return &c, true
	case *MergeWholeRowRef:
		c := *x
		return &c, true
	case *ExtractExpr:
		c := *x
		return &c, true
	case *IsNullExpr:
		c := *x
		return &c, true
	case *IsBoolExpr:
		c := *x
		return &c, true
	case *CollateExpr:
		c := *x
		return &c, true
	case *CastExpr:
		c := *x
		return &c, true
	case *UnaryOp:
		c := *x
		return &c, true
	case *BinaryOp:
		c := *x
		return &c, true
	case *IsDistinctFromExpr:
		c := *x
		return &c, true
	case *ArraySubqueryExpr:
		c := *x
		return &c, true

	// MultiAssignSubqRow is shared by construction (see slotSubqRow).
	// It is cloneable in isolation, but cloneExprRefs never reaches
	// it as a child — only as a root, which is what this arm serves.
	case *MultiAssignSubqRow:
		c := *x
		return &c, true

	// The Row pointer is copied, NOT deep-copied: the sharing and the
	// executor's pointer-keyed cache both depend on it staying the
	// same object.
	case *MultiAssignSubqElem:
		c := *x
		return &c, true

	// ---- slice fields: must be duplicated --------------------------
	case *FuncCall:
		c := *x
		c.Args = append([]Expr(nil), x.Args...)
		return &c, true
	case *RowExpr:
		c := *x
		c.Elems = append([]Expr(nil), x.Elems...)
		c.Types = append([]catalog.Type(nil), x.Types...)
		return &c, true
	case *CaseExpr:
		c := *x
		c.Whens = append([]CaseWhen(nil), x.Whens...)
		return &c, true
	case *InExpr:
		c := *x
		c.List = append([]Expr(nil), x.List...)
		c.Args = append([]Expr(nil), x.Args...)
		c.ParParam = append([]int(nil), x.ParParam...)
		return &c, true
	case *ExistsExpr:
		c := *x
		c.Args = append([]Expr(nil), x.Args...)
		c.ParParam = append([]int(nil), x.ParParam...)
		return &c, true
	case *SubqueryExpr:
		c := *x
		c.Args = append([]Expr(nil), x.Args...)
		c.ParParam = append([]int(nil), x.ParParam...)
		return &c, true
	}

	return nil, false
}
