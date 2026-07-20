package planner

import (
	"sync/atomic"
)

// subplan_lower.go — D4.1 (design bundle correlated-subquery-planning,
// stage S2b): the goopg analog of upstream's SS_replace_correlation_vars.
//
// After the whole statement is planned, every sublink that SURVIVED
// planning (was not unnested away) has the OuterColumnRefs inside its
// inner plan rewritten to ExecParamRef slots, and carries ParParam
// (slot IDs) + Args (host-side expressions that fill them per outer
// row). The executor's eval sites then bind the slots and run the inner
// plan WITHOUT pushing ctx.OuterRows.
//
// The pass is deliberately OPPORTUNISTIC and ATOMIC per sublink tree:
// a sublink is lowered only when every OuterColumnRef in its subtree is
// provably resolvable through the sublink chain, over a traversal that
// bails on any node or expression kind it does not model
// (subplan_lower_walk.go). Anything doubtful — refs crossing a LATERAL
// join scope (the operators_join_agg.go:270 push), refs escaping
// through an excluded sublink kind (ArraySubqueryExpr,
// MultiAssignSubqRow, row-constructor InExpr — their eval sites keep
// pushing OuterRows, ch.04 §2 scope note), refs whose Level exceeds the
// chain, or an unmodelled construct — leaves the WHOLE subtree
// untouched on the legacy stack path. Mixed lowering inside one subtree
// is never produced: a lowered eval site does not push OuterRows, so a
// half-rewritten subtree would leave surviving refs dangling.
//
// One flat slot space per statement (PG does the same — param IDs are
// global to the PlannerInfo tree). Level ≥ 2 refs forward through the
// intermediate sublink's ParParam/Args chain: the intermediate grows a
// param whose Arg is itself an ExecParamRef filled by ITS host, so an
// inner sublink's cache key (the param values) always reflects every
// outer row it truly depends on.

// subplanLowerMismatches counts sublinks whose recomputed correlation
// (len(ParParam) == 0) disagreed with the IsNonCorrelated flag computed
// at plan time. The param analysis is trusted (the flag is corrected);
// the counter exists so tests and debugging can notice drift.
var subplanLowerMismatches atomic.Int64

// SubplanLowerMismatches reports the number of IsNonCorrelated
// disagreements seen since process start. Test/debug API.
func SubplanLowerMismatches() int64 { return subplanLowerMismatches.Load() }

// paramAlloc is the per-statement flat slot allocator.
type paramAlloc struct{ next int }

func (a *paramAlloc) alloc() int { id := a.next; a.next++; return id }

// sublinkHandle unifies the three lowerable sublink node kinds.
type sublinkHandle struct {
	plan      Node
	setPlan   func(Node)
	setParams func(parParam []int, args []Expr)
	nonCorr   func() bool
	setNC     func(bool)
}

// handleFor returns a lowering handle for e, or nil when e is not a
// lowerable sublink (not a sublink at all, no plan, or an excluded
// kind — row-constructor IN stays on the stack path).
func handleFor(e Expr) *sublinkHandle {
	switch x := e.(type) {
	case *SubqueryExpr:
		if x.Plan == nil {
			return nil
		}
		return &sublinkHandle{
			plan:      x.Plan,
			setPlan:   func(n Node) { x.Plan = n },
			setParams: func(pp []int, a []Expr) { x.ParParam, x.Args = pp, a },
			nonCorr:   func() bool { return x.IsNonCorrelated },
			setNC:     func(b bool) { x.IsNonCorrelated = b },
		}
	case *ExistsExpr:
		if x.Plan == nil {
			return nil
		}
		return &sublinkHandle{
			plan:      x.Plan,
			setPlan:   func(n Node) { x.Plan = n },
			setParams: func(pp []int, a []Expr) { x.ParParam, x.Args = pp, a },
			nonCorr:   func() bool { return x.IsNonCorrelated },
			setNC:     func(b bool) { x.IsNonCorrelated = b },
		}
	case *InExpr:
		if x.Plan == nil {
			return nil
		}
		if _, isRow := x.Operand.(*RowExpr); isRow {
			return nil // evalRowConstructorInExpr — excluded eval site
		}
		return &sublinkHandle{
			plan:      x.Plan,
			setPlan:   func(n Node) { x.Plan = n },
			setParams: func(pp []int, a []Expr) { x.ParParam, x.Args = pp, a },
			nonCorr:   func() bool { return x.IsNonCorrelated },
			setNC:     func(b bool) { x.IsNonCorrelated = b },
		}
	}
	return nil
}

// lowerSubPlanParams is the pass entry: called once on the finished
// statement plan (Plan() in planner.go). Finds host-level sublinks via
// the same walker the correlation analysis uses and lowers each
// eligible subtree. Sublinks the walker cannot see keep working on the
// stack path — the pass never has to be complete, only sound.
func lowerSubPlanParams(root Node) Node {
	if root == nil {
		return root
	}
	a := &paramAlloc{}
	walkPlanExprs(root, func(e Expr) {
		if h := handleFor(e); h != nil {
			lowerSublinkTree(h, a)
		}
	})
	return root
}

// refKey identifies one distinct outer-value demand for slot dedup
// inside a single scope.
type refKey struct {
	scope  int // chain index of the scope that OWNS the value (0 = host of the outermost sublink)
	index  int
	source int16
	name   string
}

// lowerScope is one chain entry during lowering: the sublink plus the
// params it must receive from its host.
type lowerScope struct {
	h        *sublinkHandle
	slots    map[refKey]int
	parParam []int
	args     []Expr
}

// lowerSublinkTree analyses the sublink's subtree and, when every
// outer reference is resolvable, rewrites it. Atomic: on any doubt the
// subtree is left exactly as found (analysis mutates nothing).
func lowerSublinkTree(h *sublinkHandle, a *paramAlloc) {
	if !analyzeSublink(h.plan, 1) {
		return
	}
	rewriteSublinkPlan([]*lowerScope{{h: h, slots: map[refKey]int{}}}, a)
}

// analyzeSublink verifies (without mutating) that every OuterColumnRef
// in the plan of a sublink at chain depth `depth` (1 = outermost
// sublink being lowered) resolves within the lowered chain, that every
// nested sublink is either lowerable (recursed) or self-contained
// (excluded kinds), and that no LATERAL join scope intervenes.
func analyzeSublink(plan Node, depth int) bool {
	return lowerTraverseNode(plan, func(e Expr) (Expr, bool, bool) {
		switch x := e.(type) {
		case *OuterColumnRef:
			return e, true, x.Level <= depth
		case *SubqueryExpr, *ExistsExpr, *InExpr:
			if h := handleFor(e); h != nil {
				return e, true, analyzeSublink(h.plan, depth+1)
			}
			// Row-constructor InExpr with a plan: excluded,
			// self-pushing at eval time.
			if in, isIn := e.(*InExpr); isIn && in.Plan != nil {
				if !excludedRefsWithin(in.Plan, 1) {
					return e, true, false
				}
			}
			// Sublink without a plan (literal IN list): descend
			// into operand/list normally.
			return e, false, true
		case *ArraySubqueryExpr:
			if x.Plan != nil && !excludedRefsWithin(x.Plan, 1) {
				return e, true, false
			}
			return e, true, true
		case *MultiAssignSubqRow:
			if x.Plan != nil && !excludedRefsWithin(x.Plan, 1) {
				return e, true, false
			}
			return e, true, true
		case *BinaryOp:
			// LATERAL hazard is a plan-shape property, not an expr
			// property — checked below via the node hook.
		}
		return e, false, true
	}) && !planContainsLateralJoin(plan)
}

// excludedRefsWithin reports whether an excluded sublink's plan
// references nothing beyond the scopes its own runtime pushes cover.
// `pushes` counts self-pushing eval sites enclosing a ref: the
// excluded sublink itself pushes one scope, and every nested sublink
// of ANY kind inside it stays unlowered (the pass never rewrites
// inside excluded plans), so each nested one pushes as well.
func excludedRefsWithin(plan Node, pushes int) bool {
	if planContainsLateralJoin(plan) {
		return false
	}
	return lowerTraverseNode(plan, func(e Expr) (Expr, bool, bool) {
		switch x := e.(type) {
		case *OuterColumnRef:
			return e, true, x.Level <= pushes
		case *SubqueryExpr:
			if x.Plan != nil {
				return e, true, excludedRefsWithin(x.Plan, pushes+1)
			}
		case *ExistsExpr:
			if x.Plan != nil {
				return e, true, excludedRefsWithin(x.Plan, pushes+1)
			}
		case *InExpr:
			if x.Plan != nil {
				return e, true, excludedRefsWithin(x.Plan, pushes+1)
			}
		case *ArraySubqueryExpr:
			if x.Plan != nil {
				return e, true, excludedRefsWithin(x.Plan, pushes+1)
			}
		case *MultiAssignSubqRow:
			if x.Plan != nil {
				return e, true, excludedRefsWithin(x.Plan, pushes+1)
			}
		}
		return e, false, true
	})
}

// planContainsLateralJoin reports whether any Join in the subtree has
// outer references in its RIGHT child — the shape the executor runs
// with the general-LATERAL per-iteration OuterRows push
// (operators_join_agg.go:270), which makes static Level bookkeeping
// unsound. Uses walkPlanExprs (the broad walker) for DETECTION only —
// over-detection is safe (a bail), under-detection is impossible for
// shapes the lowering traversal accepts, because that traversal bails
// on every node kind walkPlanExprs does not also cover.
func planContainsLateralJoin(plan Node) bool {
	found := false
	var walk func(Node)
	walk = func(n Node) {
		if n == nil || found {
			return
		}
		if j, ok := n.(*Join); ok {
			hasOuter := false
			walkPlanExprs(j.Right, func(e Expr) {
				if _, isO := e.(*OuterColumnRef); isO {
					hasOuter = true
				}
			})
			if hasOuter {
				found = true
				return
			}
		}
		for _, c := range lowerNodeChildren(n) {
			walk(c)
		}
	}
	walk(plan)
	return found
}

// lowerNodeChildren lists the child plan nodes for the LATERAL scan.
func lowerNodeChildren(n Node) []Node {
	switch x := n.(type) {
	case *Filter:
		return []Node{x.Child}
	case *Project:
		return []Node{x.Child}
	case *Aggregate:
		return []Node{x.Child}
	case *Sort:
		return []Node{x.Child}
	case *Limit:
		return []Node{x.Child}
	case *Distinct:
		return []Node{x.Child}
	case *DistinctOn:
		return []Node{x.Child}
	case *LockRows:
		return []Node{x.Child}
	case *Join:
		return []Node{x.Left, x.Right}
	case *NestedLoopIndexJoin:
		return []Node{x.Outer, x.Inner}
	case *MultiHashJoin:
		return x.Tables
	}
	return nil
}

// slotFor returns the ParamExec slot through which chain[k] receives
// the value of outer reference `ref`, whose target scope sits `dist`
// hops above chain[k]'s plan scope (dist == ref.Level as seen from an
// expression directly inside chain[k]'s plan). Allocates and forwards
// through the chain on first demand; dedups per scope so one outer
// column referenced twice costs one slot.
func slotFor(chain []*lowerScope, k int, ref *OuterColumnRef, dist int, a *paramAlloc) int {
	sc := chain[k]
	key := refKey{scope: k - dist, index: ref.Index, source: ref.SourceTableIdx, name: ref.Name}
	if id, ok := sc.slots[key]; ok {
		return id
	}
	id := a.alloc()
	sc.slots[key] = id
	sc.parParam = append(sc.parParam, id)
	if dist == 1 {
		// Immediate host: evaluated by the eval site against the same
		// row OuterColumnRef{Level:1} used to resolve to.
		sc.args = append(sc.args, &ColumnRef{
			pos:            ref.pos,
			Index:          ref.Index,
			Name:           ref.Name,
			Type:           ref.Type,
			SourceTableIdx: ref.SourceTableIdx,
		})
	} else {
		// Forwarded deeper level: the host sublink supplies the value
		// through its own params and this scope's Arg reads that slot
		// — so Args are NOT always plain ColumnRefs (ch.04 §3.2).
		hostSlot := slotFor(chain, k-1, ref, dist-1, a)
		sc.args = append(sc.args, &ExecParamRef{pos: ref.pos, ID: hostSlot, Type: ref.Type})
	}
	return id
}

// rewriteSublinkPlan rewrites the innermost chain entry's plan:
// OuterColumnRefs become ExecParamRefs, nested lowerable sublinks
// recurse with the chain extended. Runs only after analyzeSublink
// approved the whole subtree, so every fx application succeeds by
// construction; the ok return is asserted anyway.
func rewriteSublinkPlan(chain []*lowerScope, a *paramAlloc) {
	k := len(chain) - 1
	sc := chain[k]
	ok := lowerTraverseNode(sc.h.plan, func(e Expr) (Expr, bool, bool) {
		switch x := e.(type) {
		case *OuterColumnRef:
			id := slotFor(chain, k, x, x.Level, a)
			return &ExecParamRef{pos: x.pos, ID: id, Type: x.Type}, true, true
		case *SubqueryExpr, *ExistsExpr, *InExpr:
			if h := handleFor(e); h != nil {
				rewriteSublinkPlan(append(chain, &lowerScope{h: h, slots: map[refKey]int{}}), a)
				return e, true, true
			}
			return e, false, true
		case *ArraySubqueryExpr, *MultiAssignSubqRow:
			// Excluded kinds keep their plans (and stack refs) as-is;
			// analysis proved they are self-contained.
			return e, true, true
		}
		return e, false, true
	})
	if !ok {
		// Cannot happen when analysis passed (same traversal); if it
		// ever does, the params recorded so far are still consistent
		// because slot IDs are never reused — but flag loudly.
		subplanLowerMismatches.Add(1)
		return
	}
	sc.h.setParams(sc.parParam, sc.args)
	derivedNonCorr := len(sc.parParam) == 0
	if sc.h.nonCorr() != derivedNonCorr {
		subplanLowerMismatches.Add(1)
		sc.h.setNC(derivedNonCorr)
	}
}
