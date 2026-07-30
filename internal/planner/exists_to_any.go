package planner

import (
	"os"
	"strings"
	"sync/atomic"

	"github.com/goopg/goopg/internal/parser"
)

// M0125-0036 (C3): a correlated EXISTS that cannot become a semi-join is
// re-executed once per outer row. This pass is goopg's analogue of
// upstream's convert_EXISTS_to_ANY
// (postgres/src/backend/optimizer/plan/subselect.c:1731, reached from
// make_subplan at subselect.c:263): it lifts the EXISTS body's single
// correlation equality out of the body and into an upper-level test
// expression, which makes the body UNCORRELATED and therefore hashable
// exactly once.
//
// Why this is the fix rather than caching. `0124-0004` §D4's rule says
// that when CacheMisses ≈ Calls the answer is hashed-SubPlan caching, not
// decorrelation — and per-correlation-key memoisation cannot help here,
// because the outer key (TPC-DS Q10/Q35: `c_customer_sk`) is unique per
// outer row, so every call is a miss by construction. Only removing the
// correlation makes the value set shared, and that is what PG does: its
// Q10 plan reads
//
//	Filter: ((ANY (c_customer_sk = (hashed SubPlan 2).col1)) OR
//	         (ANY (c_customer_sk = (hashed SubPlan 4).col1)))
//
// The trigger for the class is the `OR` of `EXISTS`: an OR-ed sublink can
// never become a semi-join (unnestExistsExpr requires a top-level
// conjunct), and before this pass goopg had nothing between "unnest to a
// semi-join" and "re-execute per outer row".
//
// The conversion's output rides the EXISTING hashed-SubPlan machinery:
// the rewritten node is an ordinary `InExpr{Plan: …, IsNonCorrelated:
// true}`, which executor/subplan_hash.go's evalInHashProbe already builds
// a hash table for and probes per outer row (Stage 11 / D4.3).
//
// # NULL semantics — why only qual positions, and never NOT EXISTS
//
// EXISTS is two-valued (TRUE/FALSE); `x IN (SELECT …)` is three-valued.
// The two forms differ in exactly one cell: when the operand does not
// match and the value set contains a NULL, EXISTS says FALSE and IN says
// NULL. That difference is invisible wherever NULL and FALSE select the
// same rows, which is every *qual* position — a Filter predicate, and a
// join condition of any join type (a NULL join qual is a non-match just
// like FALSE). It is NOT invisible under `NOT`, where `NOT FALSE` = TRUE
// but `NOT NULL` = NULL, so a negated EXISTS is never converted and the
// qual walk descends through `AND`/`OR` only — never through NOT, CASE,
// or a function argument. Upstream expresses the same condition as
// `isTopQual` → `subplan->unknownEqFalse` (subselect.c build_subplan).
//
// # Deliberate divergences from upstream (ledger rows, 2026-07-31)
//
//   - PG builds BOTH the plain and the hashed SubPlan and defers the
//     choice to setrefs.c (AlternativeSubPlan). goopg has no alternative-
//     subplan machinery and — per `M0125-0026` §C5 — no cardinality above
//     base scans to choose with, so the conversion is unconditional when
//     the shape matches, with GOOPG_EXISTS_TO_ANY=off as the escape.
//   - Single correlation equality only. PG's testexpr can be a ROW
//     comparison over N pairs; goopg's InExpr operand is single-column
//     (see subplan_hash.go's "goopg's IN test expressions are
//     single-column"), so a composite correlation keeps the SubPlan path.
//   - PG's subpath_is_hashable rejects a body wider than hash_mem before
//     committing to the hashed form. goopg's equivalent bound is the
//     statement's shared sublink-result budget (WorkMem/4, ch.06 D6.4),
//     which is applied at execution time by subqCachePut rather than at
//     plan time.

// existsToAnyOn is the operational kill switch. Default ON;
// GOOPG_EXISTS_TO_ANY=off at server start (or SetExistsToAnyEnabled(false)
// from tests) restores the pure per-outer-row SubPlan path. Same pattern
// as GOOPG_HASHED_SUBPLAN (executor/subplan_hash.go).
var existsToAnyOn atomic.Bool

func init() {
	existsToAnyOn.Store(os.Getenv("GOOPG_EXISTS_TO_ANY") != "off")
}

// SetExistsToAnyEnabled flips the EXISTS→ANY conversion. Test-only API;
// the operational switch is the environment variable read at init.
func SetExistsToAnyEnabled(on bool) { existsToAnyOn.Store(on) }

func existsToAnyEnabled() bool { return existsToAnyOn.Load() }

// rewriteExistsToAny runs the conversion over a finished plan tree. It is
// called from Plan() immediately before lowerSubPlanParams, i.e. after
// every rewrite that can reshape a sublink or renumber a ColumnRef: the
// operand this pass synthesises is an ordinary host-scope ColumnRef, so
// it must not be created while index-rewriting passes are still running.
func rewriteExistsToAny(root Node) Node {
	if !existsToAnyEnabled() {
		return root
	}
	rewriteExistsToAnyNode(root)
	return root
}

// rewriteExistsToAnyNode descends the plan tree and rewrites the qual of
// every qual-bearing node it recognises.
//
// The node switch is deliberately partial: a node type it does not know
// simply keeps its SubPlan (a missed optimisation, never a wrong answer),
// which is why this walker is not shared with walkPlanExprs — that one is
// an accounting walker and must be fail-closed, this one is fail-open.
func rewriteExistsToAnyNode(node Node) {
	if node == nil {
		return
	}
	switch n := node.(type) {
	case *Filter:
		rewriteExistsToAnyNode(n.Child)
		if n.Predicate != nil && n.Child != nil {
			n.Predicate = rewriteExistsToAnyQual(n.Predicate, n.Child.Output(), false)
		}
	case *Join:
		rewriteExistsToAnyNode(n.Left)
		rewriteExistsToAnyNode(n.Right)
		if n.Predicate != nil && n.Left != nil && n.Right != nil {
			n.Predicate = rewriteExistsToAnyQual(n.Predicate, joinedRowSchema(n.Left, n.Right), false)
		}
	case *NestedLoopIndexJoin:
		rewriteExistsToAnyNode(n.Outer)
		rewriteExistsToAnyNode(n.Inner)
		if n.Predicate != nil && n.Outer != nil && n.Inner != nil {
			n.Predicate = rewriteExistsToAnyQual(n.Predicate, joinedRowSchema(n.Outer, n.Inner), false)
		}
	case *MultiHashJoin:
		for _, tbl := range n.Tables {
			rewriteExistsToAnyNode(tbl)
		}
		for i, f := range n.Filters {
			if f != nil {
				n.Filters[i] = rewriteExistsToAnyQual(f, n.Output(), false)
			}
		}
	case *Project:
		rewriteExistsToAnyNode(n.Child)
	case *Aggregate:
		rewriteExistsToAnyNode(n.Child)
	case *Sort:
		rewriteExistsToAnyNode(n.Child)
	case *Limit:
		rewriteExistsToAnyNode(n.Child)
	case *Distinct:
		rewriteExistsToAnyNode(n.Child)
	case *DistinctOn:
		rewriteExistsToAnyNode(n.Child)
	case *WindowAgg:
		rewriteExistsToAnyNode(n.Child)
	case *ProjectSet:
		rewriteExistsToAnyNode(n.Child)
	case *OrdinalityWrap:
		rewriteExistsToAnyNode(n.Child)
	case *LockRows:
		rewriteExistsToAnyNode(n.Child)
	case *Gather:
		rewriteExistsToAnyNode(n.Child)
	case *GatherMerge:
		rewriteExistsToAnyNode(n.Child)
	case *CTEScan:
		rewriteExistsToAnyNode(n.Child)
	case *SetOp:
		rewriteExistsToAnyNode(n.Left)
		rewriteExistsToAnyNode(n.Right)
	case *RecursiveUnion:
		rewriteExistsToAnyNode(n.Anchor)
		rewriteExistsToAnyNode(n.Recursive)
	case *CTEDMLPrefix:
		for _, dml := range n.DMls {
			rewriteExistsToAnyNode(dml)
		}
		rewriteExistsToAnyNode(n.Body)
	}
}

// rewriteExistsToAnyQual rewrites the EXISTS sublinks of one qual.
// underOr reports whether at least one OR was crossed on the way here.
//
// It descends through AND/OR only. Everything else — NOT, CASE, a
// function argument, a comparison operand — is a position where a NULL
// result is distinguishable from FALSE, so an EXISTS found there keeps
// its two-valued SubPlan form. Sublink bodies encountered on the way are
// themselves rewritten, so a nested EXISTS inside another sublink's plan
// still gets the conversion.
//
// # Why only under an OR
//
// `M0125-0026` §C3 names the trigger precisely: the class is `EXISTS OR
// EXISTS`, and its control is Q69, whose three EXISTS are AND-ed and
// which unnestExistsExpr turns into a proper `Hash Join (ANTI)/(SEMI)`
// chain that completes. An EXISTS that IS a top-level conjunct therefore
// already has a better transformation available, and one that keeps the
// body streaming instead of materialising it; converting it here would
// pre-empt that pass on a shape it handles well. Where the semi-join
// pull-up declines for a reason of its own — a correlation folded into an
// IndexScan key, or one held in a Join.Predicate that
// collectUnnestParamsAndResiduals' Filter-only walk never inspects — the
// SubPlan survives exactly as it does today. Widening the conversion to
// those is deferred (ledger row 2026-07-31): upstream converts
// unconditionally because it can price both alternatives in setrefs.c,
// and §C5 says goopg cannot.
func rewriteExistsToAnyQual(e Expr, hostRow Schema, underOr bool) Expr {
	switch x := e.(type) {
	case *BinaryOp:
		if x.Op == parser.OpAnd || x.Op == parser.OpOr {
			or := underOr || x.Op == parser.OpOr
			x.Left = rewriteExistsToAnyQual(x.Left, hostRow, or)
			x.Right = rewriteExistsToAnyQual(x.Right, hostRow, or)
			return x
		}
	case *ExistsExpr:
		if underOr {
			if in := existsToAny(x, hostRow); in != nil {
				// The body is now uncorrelated; it may still
				// host sublinks of its own.
				rewriteExistsToAnyNode(in.Plan)
				return in
			}
		}
		rewriteExistsToAnyNode(x.Plan)
		return x
	}
	// Not an AND/OR spine node and not a convertible EXISTS: descend into
	// any sublink bodies it carries, but do not convert anything at this
	// position.
	walkExprTree(e, func(sub Expr) {
		switch s := sub.(type) {
		case *ExistsExpr:
			rewriteExistsToAnyNode(s.Plan)
		case *InExpr:
			rewriteExistsToAnyNode(s.Plan)
		case *SubqueryExpr:
			rewriteExistsToAnyNode(s.Plan)
		case *ArraySubqueryExpr:
			rewriteExistsToAnyNode(s.Plan)
		}
	})
	return e
}

// existsToAny returns the ANY-sublink form of a correlated EXISTS, or nil
// when the shape does not qualify. The ExistsExpr's body is mutated in
// place on success (the ExistsExpr itself is discarded by the caller);
// every check that can fail runs BEFORE the first mutation, so a decline
// leaves the body untouched.
func existsToAny(ex *ExistsExpr, hostRow Schema) *InExpr {
	if ex.Plan == nil || ex.IsNonCorrelated || ex.Negated || len(hostRow) == 0 {
		return nil
	}
	// The pass runs before lowerSubPlanParams, so a lowered body means
	// some other path already claimed this sublink. Belt only.
	if len(ex.ParParam) > 0 {
		return nil
	}
	// The body's own spine must not change which rows the value set
	// contains. Aggregate / Distinct / Sort / Limit are all rejected
	// outright — upstream's simplify_EXISTS_query strips ORDER BY and
	// LIMIT instead, but refusing is strictly safer and none of the C3
	// members carry one (ledger row 2026-07-31).
	if !existsBodySpineSimple(ex.Plan) {
		return nil
	}

	// Exactly one correlation reference in the body's own scope, at
	// Level 1 (the immediate host). Deeper sublinks may keep their own
	// references as long as none reaches PAST the body — the same
	// accounting rule collectUnnestParamsAndResiduals applies, and for
	// the same reason: after the conversion the host row is no longer on
	// the OuterRows stack when the body runs.
	var outerRefs []*OuterColumnRef
	escapes := false
	walkPlanExprsDeep(ex.Plan, 0, func(e Expr, planDepth int) {
		o, ok := e.(*OuterColumnRef)
		if !ok {
			return
		}
		if planDepth == 0 {
			outerRefs = append(outerRefs, o)
			return
		}
		if o.Level > planDepth {
			escapes = true
		}
	})
	if escapes || len(outerRefs) != 1 || outerRefs[0].Level != 1 {
		return nil
	}
	outerRef := outerRefs[0]

	// The reference must sit in a top-level equality conjunct of the
	// body's OWN qual holder. Two conditions ride on "own": the conjunct
	// must be removable (a reference folded into an IndexScan key, or
	// buried under an OR inside the body, is not), and the sub-column's
	// index must be readable in the holder's output coordinates so the
	// projection below can name it.
	base, pred := existsBodyQualHolder(ex.Plan)
	if base == nil {
		return nil
	}
	var pair *BinaryOp
	var subCol *ColumnRef
	for _, c := range splitAnd(pred) {
		bin, ok := c.(*BinaryOp)
		if !ok || bin.Op != parser.OpEq {
			continue
		}
		o, col := extractEquijoinPair(bin.Left, bin.Right)
		if o == outerRef && col != nil {
			pair, subCol = bin, col
			break
		}
	}
	if pair == nil {
		return nil
	}

	// The sub-column must resolve — by index AND by name — in the
	// holder's output schema, which is the row the projection added
	// below is evaluated against. Name-check as well as bounds-check:
	// an in-range index pointing at the wrong column is the silent case
	// (the same reasoning as unnestExistsExpr's R3-4 validation).
	baseSchema := base.Output()
	if subCol.Index < 0 || subCol.Index >= len(baseSchema) ||
		!strings.EqualFold(baseSchema[subCol.Index].Name, subCol.Name) {
		return nil
	}

	// Re-resolve the OUTER side against the row this qual is actually
	// evaluated against. OuterColumnRef.Index was set by the binder
	// against FROM-cumulative offsets, and MultiHashJoin packing re-sorts
	// its output schema by OID while treating a sublink body as opaque —
	// so the index inside the body can be stale by the time this pass
	// runs, and reading the stale slot yields a value set that matches
	// nothing (TPC-DS Q35: 0 rows instead of 100, found by probe before
	// this resolve existed). Same hazard, same remedy as M0071-0003 in
	// unnestExistsExpr's residual lifting — except that a decline here is
	// free, so ambiguity is refused rather than resolved by fallback.
	operandIdx, ok := resolveHostOperandIdx(hostRow, outerRef)
	if !ok {
		return nil
	}

	// --- every decline is behind us; mutate ---------------------------

	remaining := make([]Expr, 0, 4)
	for _, c := range splitAnd(pred) {
		if c != Expr(pair) {
			remaining = append(remaining, c)
		}
	}
	newPred := combineAnd(remaining)
	if newPred == nil {
		// All conjuncts consumed. `true` rather than nil: a nil Join
		// predicate is how a CROSS join is spelled, and downstream
		// passes read it that way.
		newPred = &BooleanConst{pos: pair.Pos(), Value: true}
	}
	switch h := base.(type) {
	case *Filter:
		h.Predicate = newPred
	case *Join:
		h.Predicate = newPred
	}

	// Project the correlation column: the hashed probe reads one value
	// per row (collectInValues rejects any other width).
	projected := &Project{
		pos:   ex.Pos(),
		Child: base,
		Targets: []Expr{&ColumnRef{
			pos:            subCol.Pos(),
			Index:          subCol.Index,
			Name:           subCol.Name,
			Type:           subCol.Type,
			SourceTableIdx: subCol.SourceTableIdx,
		}},
		schema: Schema{baseSchema[subCol.Index]},
	}

	return &InExpr{
		pos: ex.Pos(),
		Operand: &ColumnRef{
			pos:            outerRef.Pos(),
			Index:          operandIdx,
			Name:           outerRef.Name,
			Type:           outerRef.Type,
			SourceTableIdx: outerRef.SourceTableIdx,
		},
		Plan:            projected,
		IsNonCorrelated: true,
	}
}

// joinedRowSchema is the row a join's predicate is evaluated against:
// the left side's columns followed by the right side's. It is NOT the
// join's Output() — a SEMI/ANTI join publishes only its left input, but
// its predicate still sees the padded row (the convention
// unnestExistsExpr encodes as `outerWidth + innerIndex`).
func joinedRowSchema(left, right Node) Schema {
	ls, rs := left.Output(), right.Output()
	out := make(Schema, 0, len(ls)+len(rs))
	out = append(out, ls...)
	out = append(out, rs...)
	return out
}

// resolveHostOperandIdx locates the outer reference's column in the row
// the host qual is evaluated against, and reports false when it cannot
// do so unambiguously.
//
// It is deliberately stricter than resolveOuterSchemaIdx (unnest.go),
// which returns the caller's stale index as a last resort: that fallback
// exists because its callers have already committed to a rewrite by the
// time they resolve. This pass has not — declining leaves a correct, if
// slow, SubPlan — so an ambiguous name is a decline, not a guess.
func resolveHostOperandIdx(hostRow Schema, ref *OuterColumnRef) (int, bool) {
	// Exact hit at the recorded index, confirmed by name and (when the
	// binder recorded one) by FROM-binding identity.
	if ref.Index >= 0 && ref.Index < len(hostRow) &&
		strings.EqualFold(hostRow[ref.Index].Name, ref.Name) &&
		(ref.SourceTableIdx == 0 || hostRow[ref.Index].SourceTableIdx == ref.SourceTableIdx) {
		return ref.Index, true
	}
	// SourceTableIdx disambiguates self-joins, where Name alone cannot.
	if ref.SourceTableIdx != 0 {
		found, n := -1, 0
		for i, c := range hostRow {
			if strings.EqualFold(c.Name, ref.Name) && c.SourceTableIdx == ref.SourceTableIdx {
				found, n = i, n+1
			}
		}
		if n == 1 {
			return found, true
		}
		return 0, false
	}
	found, n := -1, 0
	for i, c := range hostRow {
		if strings.EqualFold(c.Name, ref.Name) {
			found, n = i, n+1
		}
	}
	if n == 1 {
		return found, true
	}
	return 0, false
}

// existsBodySpineSimple reports whether the EXISTS body's own spine is a
// plain restriction over a scan/join — the only shape whose row set is
// unchanged by dropping the EXISTS "at least one row" semantics in favour
// of a full value set.
//
// A single non-isolated Project is tolerated because it is discarded
// (existsBodyQualHolder returns its child): an EXISTS body's target list
// is meaningless, exactly as upstream's simplify_EXISTS_query asserts
// when it throws the targetlist away.
func existsBodySpineSimple(node Node) bool {
	switch n := node.(type) {
	case *Project:
		if n.IsolatedScope {
			return false
		}
		return existsBodySpineSimple(n.Child)
	case *Filter, *Join:
		return true
	}
	return false
}

// existsBodyQualHolder returns the node that carries the body's own qual
// and that qual, peeling one non-isolated Project. Returns (nil, nil) for
// any other shape.
//
// The holder's Output() must be the row its predicate is evaluated
// against, because the caller reads the correlation column's index in
// those coordinates. That holds for *Filter (predicate over Child's row,
// and Output() == Child's schema) and for an inner *Join (predicate over
// left++right, which is its schema). It does NOT hold for a SEMI/ANTI
// join, whose Output() drops the right side — hence the join-type gate.
func existsBodyQualHolder(node Node) (Node, Expr) {
	if p, ok := node.(*Project); ok && !p.IsolatedScope {
		node = p.Child
	}
	switch n := node.(type) {
	case *Filter:
		if n.Predicate == nil {
			return nil, nil
		}
		return n, n.Predicate
	case *Join:
		if n.Predicate == nil || n.Type != JoinTypeInner {
			return nil, nil
		}
		return n, n.Predicate
	}
	return nil, nil
}
