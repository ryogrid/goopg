package optimizer

import (
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// pulledsublink.go — M0146-0015c slice 2: carry a correlated SubPlan inside a
// pulled-up body qual.
//
// PG's pull_up_sublinks_qual_recurse converts the OUTER sublink first and
// leaves a nested sublink it cannot (or must not) convert as a SubPlan inside
// the pulled-up qual — `a … EXISTS (b … EXISTS (c WHERE c.x = a.y AND
// c.z = b.w))` plans as `Hash Semi Join` with the inner EXISTS in the Join
// Filter (see docs/design/0100-0149/m0146-0015c-nested-exists-scopes.md and
// analysis/m0146/m0146-0015c/pg-oracle.txt). make_subplan then delivers the
// correlation to that SubPlan as PARAM_EXEC values evaluated against the join
// row — which is exactly what this file does for the goopg plan shape, in two
// steps:
//
//  1. At qual-rebase time (rebasePulledQual's post-pass) the kept sublink's
//     plan is CLONED — the original stays attached to the untouched conjunct
//     the declined-search fallback runs — and every escaping OuterColumnRef
//     inside the clone is rewritten to an ExecParamRef carrying a sentinel id.
//     Each escape becomes an Arg expression in problem-space coordinates: the
//     Args are same-scope children of the sublink expr, so relidsOfExpr counts
//     them toward the clause's relset (the join-search attribution) and
//     translateToLayout re-bases them to whichever node layout the qual lands
//     on, together with the rest of the qual. Sentinel ids are NEGATIVE
//     (-(i+1) for Args[i]): they can never collide with the positive ids
//     paramAlloc mints post-plan.
//  2. lowerSubPlanParams — which runs once on the finished statement plan —
//     renumbers each sentinel block into the flat per-statement ParamExec
//     space, and skips pre-lowered sublinks (any non-empty ParParam) so the
//     ordinary lowering pass cannot clobber the binding.
//
// The escaping-ref math mirrors rebasePulledQual's qual-level arm, shifted by
// the kept sublink's own scope: an OuterColumnRef at link-depth d (d nested
// sublinks descended inside the kept plan) with Level > d escapes the kept
// host; hops = Level - d - 1 counts scopes above the pulled body — 0 lands on
// the body itself (problem-space position via bodyLeafOf/pullSpans), hops
// through the ancestor chain land on ancestor bodies (allSpans/bodyBase), one
// past the topmost body is the emitting scope, and anything deeper cannot be
// reached and stays a refusal. References with Level <= d read frames pushed
// by the (unlowered) nested sublinks inside the kept plan and are untouched.

// keptArgKey identifies one escaping outer-value demand for Arg dedup inside
// a single kept sublink: same scope + same column = same slot.
type keptArgKey struct {
	hops   int // scopes above the kept sublink's host (the pulled body)
	index  int
	source int16
	name   string
}

// keptScopeArgs accumulates one kept sublink's ParParam/Args pairing while its
// plan is being rebased.
type keptScopeArgs struct {
	args  []Expr
	slots map[keptArgKey]int
}

func (s *keptScopeArgs) slotFor(key keptArgKey, arg Expr) int {
	if s.slots == nil {
		s.slots = map[keptArgKey]int{}
	}
	if id, ok := s.slots[key]; ok {
		return id
	}
	id := len(s.args)
	s.slots[key] = id
	s.args = append(s.args, arg)
	return id
}

// keptRebase carries the problem-space context the kept-plan rewrite shares
// with rebasePulledQual: the pulled body's own leaf spans, the ancestor
// chain's spans, and the emitting scope's width.
type keptRebase struct {
	pb            *jtPulledBody
	pullSpans     []leafSpan
	emittingTotal int
	ctx           *resolveContext
	allSpans      []leafSpan
	bodyBase      func(*jtPulledBody) (int, bool)
	why           string
}

// escapeArg turns one escaping OuterColumnRef into the problem-space
// ColumnRef the join row will deliver. hops counts scopes above the pulled
// body: 0 is the body itself, hops through the parent chain are ancestor
// bodies, and exactly one hop past the topmost body is the emitting scope.
func (rc *keptRebase) escapeArg(r *OuterColumnRef, hops int) (*ColumnRef, bool) {
	anc := rc.pb
	walked := 0
	for walked < hops && anc != nil {
		anc = anc.parent
		walked++
	}
	if anc != nil {
		base, okBase := rc.bodyBase(anc)
		leaf, local, found := bodyLeafOf(anc, r.Index)
		if !okBase || !found {
			rc.why = "kept-ancestor-column"
			return nil, false
		}
		src := r.SourceTableIdx
		if src != 0 {
			src += anc.srcOffset
		}
		return &ColumnRef{
			pos: r.pos, Index: rc.allSpans[base+leaf].lo + local,
			Name: r.Name, Type: r.Type, SourceTableIdx: src,
		}, true
	}
	// Past the topmost body the emitting scope is exactly `walked` hops
	// above pb — the same rule rebasePulledQual's off-the-top arm applies
	// for a top-level body (hops == walked == 1 there). Anything deeper
	// points above the emitting statement and stays unbindable.
	if hops != walked {
		rc.why = "kept-deep-ref"
		return nil, false
	}
	if r.Index < 0 || r.Index >= rc.emittingTotal {
		rc.why = "kept-outer-index"
		return nil, false
	}
	if b, okB := emittingBindingAt(rc.ctx, r.Index); !okB ||
		(b.sourceIdx != 0 && r.SourceTableIdx != 0 && b.sourceIdx != r.SourceTableIdx) {
		rc.why = "kept-outer-binding"
		return nil, false
	}
	return &ColumnRef{
		pos: r.pos, Index: r.Index, Name: r.Name, Type: r.Type,
		SourceTableIdx: r.SourceTableIdx,
	}, true
}

// rebaseNode rewrites the escaping OuterColumnRefs inside the kept plan's
// clone. linkDepth is the number of nested sublink boundaries crossed inside
// the kept plan; refs at Level <= linkDepth read frames an unlowered nested
// eval still pushes and are left alone, deeper levels escape and become
// ExecParamRef sentinels. The switch mirrors analyzeSublink's traversal
// coverage — the admission check uses the same shape, so a plan it admitted
// cannot hit an unmodelled corner here — except that a nested sublink already
// carrying params (pre-lowered by its own statement's pull-up) is skipped:
// its block is self-contained and its subtree holds no raw OuterColumnRef
// reaching this far.
func (rc *keptRebase) rebaseNode(n Node, linkDepth int, sc *keptScopeArgs) bool {
	var fx lowerExprFn
	fx = func(e Expr) (Expr, bool, bool) {
		switch x := e.(type) {
		case *OuterColumnRef:
			if x.Level <= linkDepth {
				return e, true, true
			}
			hops := x.Level - linkDepth - 1
			arg, okArg := rc.escapeArg(x, hops)
			if !okArg {
				return e, true, false
			}
			s := sc.slotFor(keptArgKey{
				hops: hops, index: x.Index, source: x.SourceTableIdx, name: x.Name,
			}, arg)
			return &ExecParamRef{pos: x.pos, ID: -(s + 1), Type: x.Type}, true, true
		case *ExecParamRef:
			// An already-lowered deeper block's ref — left alone.
			return e, true, true
		case *SubqueryExpr, *ExistsExpr, *InExpr:
			if h := handleFor(e); h != nil {
				if len(h.params()) > 0 {
					return e, true, true
				}
				if !rc.rebaseNode(h.plan, linkDepth+1, sc) {
					return e, true, false
				}
				if in, isIn := e.(*InExpr); isIn {
					// Operand and literal List evaluate in this scope,
					// not inside the sublink — same handling the D4.1
					// traversal gives them (analyzeSublink's S4b arm).
					if in.Operand != nil {
						ne, okE := lowerTraverseExpr(in.Operand, fx)
						if !okE {
							return e, true, false
						}
						in.Operand = ne
					}
					for i, le := range in.List {
						ne, okE := lowerTraverseExpr(le, fx)
						if !okE {
							return e, true, false
						}
						in.List[i] = ne
					}
				}
				return e, true, true
			}
			// Excluded eval site (row-constructor IN): refs must stay
			// within the frames its own pushes cover.
			if in, isIn := e.(*InExpr); isIn && in.Plan != nil {
				return e, true, excludedRefsWithin(in.Plan, linkDepth+1)
			}
			return e, false, true
		case *ArraySubqueryExpr:
			if x.Plan != nil && !excludedRefsWithin(x.Plan, linkDepth+1) {
				return e, true, false
			}
			return e, true, true
		case *MultiAssignSubqRow:
			if x.Plan != nil && !excludedRefsWithin(x.Plan, linkDepth+1) {
				return e, true, false
			}
			return e, true, true
		}
		return e, false, true
	}
	if !lowerTraverseNode(n, fx) {
		if rc.why == "" {
			rc.why = "kept-subplan-unmodelled"
		}
		return false
	}
	return true
}

// rebaseOne clones one kept sublink's plan, rewrites its escaping refs, and
// installs the sentinel ParParam/Args on the (cloned) sublink expr. An
// uncorrelated kept sublink gets the clone with no params — the clone is still
// required because the plan pointer is shared with the fallback's conjunct.
func (rc *keptRebase) rebaseOne(x Expr, h *sublinkHandle) bool {
	cl, err := clonePlanReplacingOuter(h.plan, nil)
	if err != nil || cl == nil {
		rc.why = "kept-subplan-clone"
		return false
	}
	sc := &keptScopeArgs{}
	if !rc.rebaseNode(cl, 0, sc) {
		return false
	}
	h.setPlan(cl)
	if len(sc.args) > 0 {
		pp := make([]int, len(sc.args))
		for i := range pp {
			pp[i] = -(i + 1)
		}
		h.setParams(pp, sc.args)
		h.setNC(false)
	}
	return true
}

// rebaseQualKeptSubplans is the post-pass rebasePulledQual runs over its
// rebased qual: every sublink-carrying expr either gets its plan cloned and
// re-based (lowerable kinds) or is verified correlation-free (excluded kinds,
// which cannot be re-based and therefore still refuse the shape they refused
// under OnScope's planHasOuterRef check).
func rebaseQualKeptSubplans(q Expr, pb *jtPulledBody, pullSpans []leafSpan, emittingTotal int, ctx *resolveContext, allSpans []leafSpan, bodyBase func(*jtPulledBody) (int, bool)) bool {
	rc := &keptRebase{
		pb: pb, pullSpans: pullSpans, emittingTotal: emittingTotal,
		ctx: ctx, allSpans: allSpans, bodyBase: bodyBase,
	}
	failed := false
	ok := walkExprRefs(q, scopeIgnore, exprVisitor{
		// Visit's false return PRUNES, it does not abort — walkExprRefs
		// still reports ok. Failure has to travel on `failed`, the same
		// flag-and-prune shape rebasePulledQual's rewriter uses.
		Visit: func(x Expr) bool {
			if failed {
				return false
			}
			plans := ExprSubplans(x)
			if len(plans) == 0 {
				return true
			}
			h := handleFor(x)
			if h == nil {
				// Excluded eval-site kind: same refusal the old OnScope
				// check gave — a correlated excluded subplan cannot be
				// re-based into problem space.
				for _, p := range plans {
					if planHasOuterRef(p) {
						rc.why = "subplan-correlated"
						failed = true
						return false
					}
				}
				return true
			}
			if !rc.rebaseOne(x, h) {
				failed = true
				return false
			}
			// Any inner-plan slot not owned by the handle (there are none
			// today — ExprSubplans lists the same single .Plan — but the
			// enumeration is the contract, not the count) must still be
			// correlation-free.
			for _, p := range plans {
				if p == h.plan {
					continue
				}
				if planHasOuterRef(p) {
					rc.why = "subplan-correlated"
					failed = true
					return false
				}
			}
			return true
		},
		OnUnknown: func(x Expr) {
			rc.why = "unknown-" + exprTypeName(x)
		},
	})
	if !ok || failed {
		if rc.why == "" {
			rc.why = "kept-subplan-walk"
		}
		noteRebaseFail(rc.why)
		return false
	}
	return true
}

// keptSubplanAdmissible is bodyQualsAdmitSublinkList's per-sublink gate for a
// lowerable kind: the plan must clone (the pulled arm never shares plan
// structure it is about to mutate), must not contain a LATERAL join (static
// Level bookkeeping is unsound across it — the same reason analyzeSublink
// refuses), must not evaluate volatile functions at a different rate than the
// subplan path would have, and every escaping outer ref must land on a scope
// the problem space still contains — the body itself, one of its pulled
// ancestors, or the emitting scope.
func keptSubplanAdmissible(e Expr, bodyDepth int, cat catalog.Catalog) (string, bool) {
	h := handleFor(e)
	if h == nil {
		return "", true
	}
	if !planCloneSupported(h.plan) {
		return "nested-sublink-uncloneable", false
	}
	if planContainsLateralJoin(h.plan) {
		return "nested-sublink-lateral", false
	}
	if planHasVolatileExpr(h.plan, cat) {
		return "nested-sublink-volatile", false
	}
	if !keptPlanRefsAdmissible(h.plan, bodyDepth) {
		return "nested-sublink-deep-ref", false
	}
	return "", true
}

// keptPlanRefsAdmissible is the check half of rebaseNode: same traversal,
// same scope arithmetic, no mutation. hops counts scopes above the pulled
// body — bodyDepth ancestors plus the emitting scope are reachable, anything
// deeper is not.
func keptPlanRefsAdmissible(plan Node, bodyDepth int) bool {
	var check func(n Node, linkDepth int) bool
	check = func(n Node, linkDepth int) bool {
		var fx lowerExprFn
		fx = func(e Expr) (Expr, bool, bool) {
			switch x := e.(type) {
			case *OuterColumnRef:
				if x.Level <= linkDepth {
					return e, true, true
				}
				return e, true, x.Level-linkDepth-1 <= bodyDepth+1
			case *SubqueryExpr, *ExistsExpr, *InExpr:
				if h := handleFor(e); h != nil {
					if len(h.params()) > 0 {
						return e, true, true
					}
					if !check(h.plan, linkDepth+1) {
						return e, true, false
					}
					if in, isIn := e.(*InExpr); isIn {
						if in.Operand != nil {
							if _, okE := lowerTraverseExpr(in.Operand, fx); !okE {
								return e, true, false
							}
						}
						for _, le := range in.List {
							if _, okE := lowerTraverseExpr(le, fx); !okE {
								return e, true, false
							}
						}
					}
					return e, true, true
				}
				if in, isIn := e.(*InExpr); isIn && in.Plan != nil {
					return e, true, excludedRefsWithin(in.Plan, linkDepth+1)
				}
				return e, false, true
			case *ArraySubqueryExpr:
				if x.Plan != nil {
					return e, true, excludedRefsWithin(x.Plan, linkDepth+1)
				}
				return e, true, true
			case *MultiAssignSubqRow:
				if x.Plan != nil {
					return e, true, excludedRefsWithin(x.Plan, linkDepth+1)
				}
				return e, true, true
			}
			return e, false, true
		}
		return lowerTraverseNode(n, fx)
	}
	return check(plan, 0)
}

// --- sentinel renumbering (lowerSubPlanParams) -----------------------------

// blockNeedsRenumber reports whether the sublink's ParParam block still holds
// pull-up sentinel ids (any negative).
func blockNeedsRenumber(h *sublinkHandle) bool {
	for _, id := range h.params() {
		if id < 0 {
			return true
		}
	}
	return false
}

// renumberPulledSubplanParams finds every pre-lowered sublink the pull-up
// left in the finished statement plan and renumbers its sentinel ParParam
// block into the statement's flat ParamExec space via `a`. Runs before the
// ordinary lowering walk so the fresh ids can never collide, and descends
// into inner plans because a kept sublink can sit anywhere the pulled qual
// was carried — including inside an excluded eval site's plan.
func renumberPulledSubplanParams(n Node, a *paramAlloc) {
	switch x := n.(type) {
	case *Update:
		n = x.Child
	case *Delete:
		n = x.Child
	case *Insert:
		n = x.Source
	}
	if n == nil {
		return
	}
	walkPlanExprs(n, func(e Expr) { descendSublinkParams(e, a) })
}

// descendSublinkParams renumbers the block of e's sublink when it is
// pre-lowered, or descends into e's inner plans otherwise — a kept sublink
// inside a nested subplan's plan is only reachable this way.
func descendSublinkParams(e Expr, a *paramAlloc) {
	if h := handleFor(e); h != nil {
		if blockNeedsRenumber(h) {
			if !renumberPulledBlock(h, a) {
				// The pull-up only emits sentinel blocks inside plans its
				// own traversal admitted; a block this walk cannot finish
				// means the coverage contract broke — say so loudly rather
				// than letting ParamExec[-1] dangle at execution.
				panic("lowerSubPlanParams: pulled-subplan param renumber failed")
			}
			return
		}
		renumberPulledSubplanParams(h.plan, a)
		return
	}
	switch x := e.(type) {
	case *InExpr:
		// Row-constructor IN keeps its plan on the excluded stack path;
		// a kept sublink inside it still needs its block renumbered.
		if x.Plan != nil {
			renumberPulledSubplanParams(x.Plan, a)
		}
	case *ArraySubqueryExpr:
		if x.Plan != nil {
			renumberPulledSubplanParams(x.Plan, a)
		}
	case *MultiAssignSubqRow:
		if x.Plan != nil {
			renumberPulledSubplanParams(x.Plan, a)
		}
	case *MultiAssignSubqElem:
		if x.Row != nil && x.Row.Plan != nil {
			renumberPulledSubplanParams(x.Row.Plan, a)
		}
	}
}

// renumberPulledBlock maps one pre-lowered block's sentinels onto fresh
// statement slots and rewrites every matching ExecParamRef inside the block's
// plan — descending through unlowered nested sublinks (whose escaping refs
// point at THIS block's slots) while letting nested pre-lowered blocks
// renumber their own space.
func renumberPulledBlock(h *sublinkHandle, a *paramAlloc) bool {
	pp := h.params()
	m := make(map[int]int, len(pp))
	for _, id := range pp {
		m[id] = a.alloc()
	}
	if !renumberDeep(h.plan, m, a) {
		return false
	}
	for i, id := range pp {
		pp[i] = m[id]
	}
	return true
}

// renumberDeep rewrites ExecParamRef sentinel ids via m across n, recursing
// into sublink plans the way rebaseNode does: an unlowered nested plan keeps
// the enclosing map (its sentinels belong to the enclosing block), a nested
// pre-lowered block renumbers itself.
func renumberDeep(n Node, m map[int]int, a *paramAlloc) bool {
	var fx lowerExprFn
	fx = func(e Expr) (Expr, bool, bool) {
		switch x := e.(type) {
		case *ExecParamRef:
			if x.ID < 0 {
				f, found := m[x.ID]
				if !found {
					// A sentinel this block does not own — the pull-up
					// writes sentinels only for scopes it rebased; finding
					// one means the block bookkeeping drifted.
					return e, true, false
				}
				return &ExecParamRef{pos: x.pos, ID: f, Type: x.Type}, true, true
			}
			return e, true, true
		case *SubqueryExpr, *ExistsExpr, *InExpr:
			if h := handleFor(e); h != nil {
				if blockNeedsRenumber(h) {
					return e, true, renumberPulledBlock(h, a)
				}
				if !renumberDeep(h.plan, m, a) {
					return e, true, false
				}
				if in, isIn := e.(*InExpr); isIn {
					if in.Operand != nil {
						ne, okE := lowerTraverseExpr(in.Operand, fx)
						if !okE {
							return e, true, false
						}
						in.Operand = ne
					}
					for i, le := range in.List {
						ne, okE := lowerTraverseExpr(le, fx)
						if !okE {
							return e, true, false
						}
						in.List[i] = ne
					}
				}
				return e, true, true
			}
			if in, isIn := e.(*InExpr); isIn && in.Plan != nil {
				return e, true, renumberDeep(in.Plan, m, a)
			}
			return e, false, true
		case *ArraySubqueryExpr:
			if x.Plan != nil {
				return e, true, renumberDeep(x.Plan, m, a)
			}
			return e, true, true
		case *MultiAssignSubqRow:
			if x.Plan != nil {
				return e, true, renumberDeep(x.Plan, m, a)
			}
			return e, true, true
		}
		return e, false, true
	}
	return lowerTraverseNode(n, fx)
}

// ---------------------------------------------------------------------------
// M0146-0015c slice 3 — convert_EXISTS_to_ANY for kept sublinks
//
// PG's convert_EXISTS_to_ANY (subselect.c:1731, reached from make_subplan at
// :263) turns an EXISTS whose correlation clauses are hash-equalities into an
// ANY sublink over the correlated columns: the inner side of every
// `outervar = innervar` pair becomes a target of the (now decorrelated) body
// and the outer side becomes the parent's testexpr — a hashed SubPlan at
// execution. goopg's kept-subplan form arrives here already pre-lowered: the
// escaping OuterColumnRefs were rewritten to negative-sentinel ExecParamRefs
// and the outer expressions sit in Args. The conversion therefore maps each
// sentinel pair to one projected column plus one operand element instead of
// minting Params; the produced InExpr is non-correlated, carries no ParParam
// binding at all, and gets UnknownEqFalse — subplan->unknownEqFalse — because
// its source was a two-valued EXISTS in a qual position, which is what lets
// the executor's tuple probe answer FALSE where a parser-written IN would
// owe NULL (no partial-match table needed).
//
// Every check runs BEFORE the first mutation, exactly as in existsToAny:
// a decline leaves the (correct, pre-lowered) EXISTS form untouched.

// keptExistsToAnyQual runs the conversion over a rebased pulled qual. It is
// deliberately fail-OPEN: a traversal bail or an unconvertible EXISTS leaves
// the slice-2 form in place (a missed rendering improvement, never a wrong
// answer), so a partial rewrite is still a coherent qual.
func keptExistsToAnyQual(q Expr) Expr {
	out, _ := lowerTraverseExpr(q, func(e Expr) (Expr, bool, bool) {
		switch x := e.(type) {
		case *ExistsExpr:
			if in := keptExistsToAny(x); in != nil {
				return in, true, true
			}
			return e, true, true
		case *InExpr, *SubqueryExpr, *ArraySubqueryExpr,
			*MultiAssignSubqRow, *MultiAssignSubqElem:
			// Sublink-bearing kinds are opaque to this pass — their plans
			// are inner scope, and the generic descent would bail on them
			// anyway (lowerTraverseExpr's `x.Plan != nil` arm).
			return e, true, true
		}
		return e, false, true
	})
	return out
}

// keptExistsToAny returns the ANY-sublink form of a kept, pre-lowered
// EXISTS, or nil when the shape does not qualify. Mirrors
// convert_EXISTS_to_ANY's checks against the slice-2 representation:
//
//   - every escaping ref (negative-sentinel ExecParamRef) must sit in a
//     top-level `innercol = sentinel` conjunct of the body's own qual
//     holder — the pair extraction PG does over the WHERE clause;
//   - the inner side must be a plain ColumnRef resolvable by index AND
//     name in the holder's output (complex inner exprs keep the EXISTS
//     form — same bound existsToAny applies);
//   - no sentinel may remain anywhere at body level once the equality
//     conjuncts are removed — PG's `contain_vars_of_level(newWhere,1)`
//     and `contain_vars_of_level(rightargs,1)` refusals, expressed
//     against the sentinel set.
//
// Volatility needs no re-check: keptSubplanAdmissible already required
// planHasVolatileExpr == false on this exact body.
func keptExistsToAny(ex *ExistsExpr) *InExpr {
	if ex == nil || ex.Plan == nil || ex.IsNonCorrelated ||
		len(ex.ParParam) == 0 || len(ex.Args) != len(ex.ParParam) {
		return nil
	}
	for _, p := range ex.ParParam {
		// Only the slice-2 sentinel block is pullable apart — a
		// non-negative id means some other path claimed this sublink.
		if p >= 0 {
			return nil
		}
	}
	if !existsBodySpineSimple(ex.Plan) {
		return nil
	}
	holder, pred := existsBodyQualHolder(ex.Plan)
	if holder == nil {
		return nil
	}
	holderSchema := holder.Output()

	type pair struct {
		arg  int
		col  *ColumnRef
		ref  *ExecParamRef
	}
	var pairs []pair
	var residual []Expr
	for _, c := range splitAnd(pred) {
		bin, ok := c.(*BinaryOp)
		if !ok || bin.Op != parser.OpEq {
			residual = append(residual, c)
			continue
		}
		var pr *ExecParamRef
		var col *ColumnRef
		if l, isRef := bin.Right.(*ExecParamRef); isRef && l.ID < 0 {
			pr = l
			col, _ = bin.Left.(*ColumnRef)
		} else if r, isRef := bin.Left.(*ExecParamRef); isRef && r.ID < 0 {
			pr = r
			col, _ = bin.Right.(*ColumnRef)
		}
		if pr == nil || col == nil {
			residual = append(residual, c)
			continue
		}
		argIdx := -pr.ID - 1
		if argIdx < 0 || argIdx >= len(ex.Args) {
			residual = append(residual, c)
			continue
		}
		if col.Index < 0 || col.Index >= len(holderSchema) ||
			!strings.EqualFold(holderSchema[col.Index].Name, col.Name) {
			// An inner side that cannot be named in holder-output
			// coordinates cannot become a projected column.
			return nil
		}
		pairs = append(pairs, pair{arg: argIdx, col: col, ref: pr})
	}
	if len(pairs) == 0 {
		return nil
	}

	// No body-level sentinel may survive outside the extracted conjuncts
	// (contain_vars_of_level over newWhere/rightargs). Pointer-identity is
	// exact: the sentinel occurrences the walk finds are the same objects
	// the conjuncts hold. Depth-0 only — a negative id inside a nested
	// sublink's plan belongs to that block's own pre-lowering.
	extracted := make(map[*ExecParamRef]bool, len(pairs))
	for _, p := range pairs {
		extracted[p.ref] = true
	}
	stray := false
	walkPlanExprsDeep(ex.Plan, 0, func(e Expr, planDepth int) {
		if planDepth != 0 {
			return
		}
		if pr, isRef := e.(*ExecParamRef); isRef && pr.ID < 0 && !extracted[pr] {
			stray = true
		}
	})
	if stray {
		return nil
	}

	// --- every decline is behind us; mutate ---------------------------

	newPred := combineAnd(residual)
	if newPred == nil {
		newPred = &BooleanConst{pos: ex.Pos(), Value: true}
	}
	switch h := holder.(type) {
	case *Filter:
		h.Predicate = newPred
	case *Join:
		h.Predicate = newPred
	}

	targets := make([]Expr, len(pairs))
	schema := make(Schema, len(pairs))
	elems := make([]Expr, len(pairs))
	types := make([]catalog.Type, len(pairs))
	for i, p := range pairs {
		targets[i] = &ColumnRef{
			pos: p.col.Pos(), Index: p.col.Index, Name: p.col.Name,
			Type: p.col.Type, SourceTableIdx: p.col.SourceTableIdx,
		}
		schema[i] = holderSchema[p.col.Index]
		elems[i] = ex.Args[p.arg]
		if cr, isCol := ex.Args[p.arg].(*ColumnRef); isCol {
			types[i] = cr.Type
		}
	}
	projected := &Project{
		pos: ex.Pos(), Child: holder, Targets: targets, schema: schema,
	}
	var operand Expr = &RowExpr{pos: ex.Pos(), Elems: elems, Types: types}
	if len(elems) == 1 {
		operand = elems[0]
	}

	// Subquery stays nil for the reason existsToAny records: this is a
	// rewrite, not a copy — the parser body no longer describes the plan.
	// Negated survives as the link's own flag: `NOT EXISTS` is PG's
	// `NOT (SubPlan)` in the qual, which converts identically — and
	// under unknownEqFalse a negated ANY can never owe NULL either.
	return &InExpr{
		pos: ex.Pos(), Operand: operand, Negated: ex.Negated, Plan: projected,
		IsNonCorrelated: true, UnknownEqFalse: true,
	}
}
