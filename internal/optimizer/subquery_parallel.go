package optimizer

// R33 (K44): post-cache parallel-pass recursion into uncorrelated
// sublink plans.
//
// The post-pass (MaybeAddGather) is the only producer that can reach
// one-relation nested shapes — one-relation statements never enter
// the path search (relfromjoinlist.go one-item early return;
// gatherpaths.go header) — but every walk in it stops at expression
// boundaries (parallelChildren follows Child links only). Sublink
// plans therefore never receive partial-aggregation splits or
// Gathers in default mode. This file recurses the pass into them.
//
// ELIGIBILITY (all must hold; anything else stays serial, the safe
// direction):
//
//   - kind is SubqueryExpr, ExistsExpr, InExpr-with-Plan, or
//     ArraySubqueryExpr (MultiAssignSubqRow is out: assignment rows,
//     and their hosts are DML the pass refuses regardless);
//   - IsNonCorrelated (correlated plans execute per outer row,
//     possibly inside workers — a Gather there multiplies workers by
//     rows);
//   - len(Args) == 0 (PARAM_EXEC bindings evaluate per outer row;
//     treated as correlated for this pass even when Plan itself is
//     uncorrelated).
//
// ROOT-SHAPE GATE: only Aggregate-rooted sublink Plans proceed, and
// only a split (Finalize/Gather/Partial) outcome is accepted — the
// one sublink shape with a priced parallel verdict
// (partialAggSplitPays). Plain-scan, Sort-rooted, and Agg-over-Gather
// outcomes stay serial: the post-pass builds them by size
// construction with no cost comparison, which over-admitted small
// subplans in measurement (see graftSubPlan).
//
// PLACEMENT SOUNDNESS. Discovery rides WalkPlanExprs, which never
// descends into a sublink's Plan or Args — so a sublink nested inside
// another sublink's operand is never eligible here (placement), while
// the SAFETY scan below still sees it (safety). The graft is
// copy-on-write at both levels (plan nodes and exprs); the shared
// plan-cache tree is never written through. A pointer-keyed memo
// preserves DAG aliasing (CTEScan shared bodies, NLI InnerMemo
// aliasing): the same input pointer always maps to the same output.
//
// NESTING RULE (conservative). At most one Gather level per
// ancestor chain: once an ancestor carries a Gather — placed by this
// pass or pre-existing from the search — nested sublinks stay
// serial. InitPlans of a parallel query may execute inside workers,
// where a nested Gather would multiply workers by workers (the N x N
// hazard the C-19d coexistence rule exists to prevent). Sibling
// sublinks are unaffected: fifteen serial InitPlans each gain their
// own Gather. Lifting this for leader-executed InitPlans is a
// follow-up with its own executor-lifecycle proof, not this round.
type sublinkGraftState struct {
	// targets maps an eligible sublink EXPR (pointer identity, as
	// yielded by WalkPlanExprs over the unmodified tree) to its
	// un-grafted inner Plan.
	targets map[Expr]Node
	// plans maps an input sublink Plan to its grafted replacement
	// (shared bodies graft once).
	plans map[Node]Node
	// nodes maps an input plan node to its grafted copy (shared
	// subtrees, e.g. CTE bodies, keep one identity).
	nodes map[Node]Node
	// unsafeSeen guards the deep safety scan against shared bodies.
	unsafeSeen map[Node]bool
}

// eligibleSublinkPlan reports the inner Plan when e is an uncorrelated,
// arg-free sublink of an in-scope kind.
func eligibleSublinkPlan(e Expr) (Node, bool) {
	switch x := e.(type) {
	case *SubqueryExpr:
		if x.IsNonCorrelated && x.Plan != nil && len(x.Args) == 0 {
			return x.Plan, true
		}
	case *ExistsExpr:
		if x.IsNonCorrelated && x.Plan != nil && len(x.Args) == 0 {
			return x.Plan, true
		}
	case *InExpr:
		if x.IsNonCorrelated && x.Plan != nil && len(x.Args) == 0 {
			return x.Plan, true
		}
	case *ArraySubqueryExpr:
		if x.IsNonCorrelated && x.Plan != nil {
			return x.Plan, true
		}
	}
	return nil, false
}

// isSublinkKind reports all six sublink shapes, eligible or not. Past
// the targets-map hit, a sublink expr is either ineligible or
// operand-nested: the graft never rewrites through one.
func isSublinkKind(e Expr) bool {
	switch e.(type) {
	case *SubqueryExpr, *ExistsExpr, *InExpr, *ArraySubqueryExpr,
		*MultiAssignSubqRow, *MultiAssignSubqElem:
		return true
	}
	return false
}

// copySublinkWithPlan returns a copy of the eligible sublink e
// pointing at newPlan. Only the four eligible kinds arrive here.
func copySublinkWithPlan(e Expr, newPlan Node) Expr {
	switch x := e.(type) {
	case *SubqueryExpr:
		c := *x
		c.Plan = newPlan
		return &c
	case *ExistsExpr:
		c := *x
		c.Plan = newPlan
		return &c
	case *InExpr:
		c := *x
		c.Plan = newPlan
		return &c
	case *ArraySubqueryExpr:
		c := *x
		c.Plan = newPlan
		return &c
	}
	return e
}

// spliceField rewrites one host expression copy-on-write, splicing
// grafted Plans into eligible sublinks found by pointer identity in
// targets. Unrecognized expr shapes and everything outside targets is
// left as found (fail closed = serial, today's behavior).
func spliceField(e Expr, st *sublinkGraftState, s ParallelSettings, anc bool) (Expr, bool) {
	if e == nil {
		return nil, false
	}
	if newPlan, ok := st.targets[e]; ok && !anc {
		gp := st.graftSubPlan(newPlan, s)
		if gp == newPlan {
			// Declined (unsafe, too small, nested under a Gather):
			// no copy, so pointer identity still proves "unchanged".
			return e, false
		}
		return copySublinkWithPlan(e, gp), true
	}
	if isSublinkKind(e) {
		// Ineligible (correlated, Args-bound, excluded kind) or
		// operand-nested: never rewrite through.
		return e, false
	}
	c, ok := shallowCloneExpr(e)
	if !ok {
		return e, false
	}
	slots, ok := exprChildSlots(c)
	if !ok {
		return e, false
	}
	changed := false
	for _, sl := range slots {
		if sl.kind != slotSameScope {
			// Never through inner plans or assignment rows: not in
			// targets by construction (WalkPlanExprs never descends
			// into sublink interiors).
			continue
		}
		ne, ch := spliceField(*sl.expr, st, s, anc)
		if ch {
			*sl.expr = ne
			changed = true
		}
	}
	if !changed {
		return e, false
	}
	return c, true
}

// graftSubPlan runs the full pass over one eligible sublink Plan,
// memoized by input pointer.
//
// PRICED-VERDICTS-ONLY GATE (R33 scope correction, 2026-09-09). The
// pass behind this gate places plain-scan Gathers by size
// construction, with no serial-vs-parallel cost comparison (the
// post-pass never prices what it builds — NewGather carries no
// PlanCost). Extending that construction into subqueries
// over-admitted: 1-row/31-row subplans gained workers PG keeps
// serial (measured: Q6 InitPlan 1, Q14 InitPlans 2/4). So an outcome
// is accepted only when it contains a split the input lacked — the
// one parallel shape whose verdict is priced
// (partialAggSplitPays): a Finalize aggregate paired with its
// Partial source. Everything else stays serial pending the
// cost-comparison round (K45 family): Sort-rooted merge verdicts
// (partialSortRootPays exists but unmeasured here), plain
// Agg-over-Gather, and plain-scan sublink Gathers. The check is
// outcome-based, not root-typed: a Project-wrapped aggregate (Q9's
// real shape) splits inside its wrapper.
func (st *sublinkGraftState) graftSubPlan(plan Node, s ParallelSettings) Node {
	if out, ok := st.plans[plan]; ok {
		return out
	}
	out := plan
	// Per-sublink safety with the pinned deep scan: the expression
	// closure, including nested sublink Plans, must hold no
	// worker-unsafe node. statementIsParallelSafe inside the recursion
	// is then redundant but harmless.
	if !subtreeHasUnsafeNodeDeep(plan, st.unsafeSeen) {
		if gp := maybeAddGatherInner(plan, s, false); gp != plan && hasFreshSplit(plan, gp) {
			out = gp
		}
	}
	st.plans[plan] = out
	return out
}

// isSplitAggregateRoot reports the priced split outcome: a Finalize
// aggregate paired with its Partial source (splitAggregate's shape).
func isSplitAggregateRoot(n Node) bool {
	a, ok := n.(*Aggregate)
	return ok && a.Mode == AggModeFinal && a.PartialSource != nil
}

// hasFreshSplit reports whether after contains a split aggregate
// (isSplitAggregateRoot) that before lacks: a parallel shape placed
// by this pass under a priced verdict. Traversal rides
// planChildNodes (reflection-complete) so Project-wrapped and
// otherwise nested splits are seen. Nested sublink interiors are
// vetted by their own gate when placed; a nested fresh split keeps
// its correctly-grafted ancestor.
func hasFreshSplit(before, after Node) bool {
	old := map[*Aggregate]bool{}
	collectSplits(before, old)
	found := false
	var walk func(Node)
	walk = func(cur Node) {
		if cur == nil || found {
			return
		}
		if a, ok := cur.(*Aggregate); ok && isSplitAggregateRoot(cur) && !old[a] {
			found = true
			return
		}
		kids, ok := planChildNodes(cur)
		if !ok {
			return
		}
		for _, k := range kids {
			walk(k)
		}
	}
	walk(after)
	return found
}

// collectSplits gathers the split aggregates under n.
func collectSplits(n Node, into map[*Aggregate]bool) {
	var walk func(Node)
	walk = func(cur Node) {
		if cur == nil {
			return
		}
		if isSplitAggregateRoot(cur) {
			into[cur.(*Aggregate)] = true
		}
		kids, ok := planChildNodes(cur)
		if !ok {
			return
		}
		for _, k := range kids {
			walk(k)
		}
	}
	walk(n)
}

// subtreeHasUnsafeNodeDeep extends subtreeHasUnsafeNode across sublink
// boundaries: any node executing inside this subtree's workers,
// including nested sublink Plans at any operand depth, must be
// worker-safe. Unenumerated expr shapes fail closed toward unsafe.
// Shared bodies are visited once.
func subtreeHasUnsafeNodeDeep(n Node, seen map[Node]bool) bool {
	if n == nil {
		return false
	}
	if subtreeHasUnsafeNode(n) {
		return true
	}
	unsafe := false
	WalkPlanExprs(n, func(e Expr) {
		if unsafe {
			return
		}
		ok := walkExprRefs(e, scopeDescend, exprVisitor{
			Visit: func(Expr) bool { return !unsafe },
			OnScope: func(p Node) {
				if p == nil || unsafe || seen[p] {
					return
				}
				seen[p] = true
				if subtreeHasUnsafeNodeDeep(p, seen) {
					unsafe = true
				}
			},
		})
		if !ok {
			// Unenumerated expr type: the closure cannot be proven
			// safe, so it is unsafe.
			unsafe = true
		}
	})
	return unsafe
}

// graftNode recurses the graft over one plan node, copy-on-write.
// Unmodelled node kinds return n unchanged: their subtrees stay
// serial (fail closed). Shared input pointers map to one output.
func graftNode(n Node, s ParallelSettings, anc bool, st *sublinkGraftState) Node {
	if n == nil {
		return nil
	}
	if out, ok := st.nodes[n]; ok {
		return out
	}
	out := graftNodeUncached(n, s, anc, st)
	st.nodes[n] = out
	return out
}

func graftNodeUncached(n Node, s ParallelSettings, anc bool, st *sublinkGraftState) Node {
	gc := func(c Node) Node { return graftNode(c, s, anc, st) }
	// gx splices one Expr field; gxs splices a []Expr field.
	gx := func(f Expr, set func(Expr)) bool {
		nf, ch := spliceField(f, st, s, anc)
		if ch {
			set(nf)
		}
		return ch
	}
	gxs := func(fs []Expr, set func([]Expr)) bool {
		if len(fs) == 0 {
			return false
		}
		cp := append([]Expr(nil), fs...)
		changed := false
		for i, f := range cp {
			if nf, ch := spliceField(f, st, s, anc); ch {
				cp[i] = nf
				changed = true
			}
		}
		if changed {
			set(cp)
		}
		return changed
	}
	gkeys := func(ks []SortKey, set func([]SortKey)) bool {
		if len(ks) == 0 {
			return false
		}
		cp := append([]SortKey(nil), ks...)
		changed := false
		for i, k := range cp {
			if k.Expr == nil {
				continue
			}
			if ne, ch := spliceField(k.Expr, st, s, anc); ch {
				cp[i].Expr = ne
				changed = true
			}
		}
		if changed {
			set(cp)
		}
		return changed
	}
	switch x := n.(type) {
	case *Project:
		c := *x
		changed := gxs(x.Targets, func(v []Expr) { c.Targets = v })
		if nc := gc(x.Child); nc != x.Child {
			c.Child = nc
			changed = true
		}
		if !changed {
			return n
		}
		return &c
	case *Filter:
		c := *x
		changed := gx(x.Predicate, func(v Expr) { c.Predicate = v })
		if nc := gc(x.Child); nc != x.Child {
			c.Child = nc
			changed = true
		}
		if !changed {
			return n
		}
		return &c
	case *Sort:
		c := *x
		changed := gkeys(x.Keys, func(v []SortKey) { c.Keys = v })
		if nc := gc(x.Child); nc != x.Child {
			c.Child = nc
			changed = true
		}
		if !changed {
			return n
		}
		return &c
	case *Limit:
		c := *x
		changed := gx(x.Limit, func(v Expr) { c.Limit = v })
		changed = gx(x.Offset, func(v Expr) { c.Offset = v }) || changed
		if nc := gc(x.Child); nc != x.Child {
			c.Child = nc
			changed = true
		}
		if !changed {
			return n
		}
		return &c
	case *Aggregate:
		c := *x
		changed := gxs(x.GroupExprs, func(v []Expr) { c.GroupExprs = v })
		changed = gxs(x.Passthrough, func(v []Expr) { c.Passthrough = v }) || changed
		if len(x.Aggs) > 0 {
			aggs := append([]AggregateCall(nil), x.Aggs...)
			for i, a := range aggs {
				ach := false
				if a.Arg != nil {
					if na, ch := spliceField(a.Arg, st, s, anc); ch {
						aggs[i].Arg = na
						ach = true
					}
				}
				if a.Arg2 != nil {
					if na, ch := spliceField(a.Arg2, st, s, anc); ch {
						aggs[i].Arg2 = na
						ach = true
					}
				}
				if len(a.ExtraArgs) > 0 {
					ea := append([]Expr(nil), a.ExtraArgs...)
					for j, e := range ea {
						if ne, ch := spliceField(e, st, s, anc); ch {
							ea[j] = ne
							ach = true
						}
					}
					if ach {
						aggs[i].ExtraArgs = ea
					}
				}
				if a.Filter != nil {
					if nf, ch := spliceField(a.Filter, st, s, anc); ch {
						aggs[i].Filter = nf
						ach = true
					}
				}
				changed = changed || ach
			}
			if changed {
				c.Aggs = aggs
			}
		}
		if nc := gc(x.Child); nc != x.Child {
			c.Child = nc
			changed = true
		}
		if !changed {
			return n
		}
		return &c
	case *Join:
		c := *x
		changed := gx(x.Predicate, func(v Expr) { c.Predicate = v })
		changed = gx(x.LeftKey, func(v Expr) { c.LeftKey = v }) || changed
		changed = gx(x.RightKey, func(v Expr) { c.RightKey = v }) || changed
		if nl := gc(x.Left); nl != x.Left {
			c.Left = nl
			changed = true
		}
		if nr := gc(x.Right); nr != x.Right {
			c.Right = nr
			changed = true
		}
		if !changed {
			return n
		}
		return &c
	case *NestedLoopIndexJoin:
		// Outer and residual predicate only. Inner is the per-row
		// parameterised probe: grafting inside it buys no measured
		// workload and risks the delicate per-row lifecycle, so it
		// stays exactly as planned (fail closed).
		c := *x
		changed := gx(x.Predicate, func(v Expr) { c.Predicate = v })
		if no := gc(x.Outer); no != x.Outer {
			c.Outer = no
			changed = true
		}
		if !changed {
			return n
		}
		return &c
	case *Result:
		c := *x
		changed := gxs(x.Targets, func(v []Expr) { c.Targets = v })
		changed = gx(x.OneTimeFilter, func(v Expr) { c.OneTimeFilter = v }) || changed
		if nc := gc(x.Child); nc != x.Child {
			c.Child = nc
			changed = true
		}
		if !changed {
			return n
		}
		return &c
	case *IndexScan:
		c := *x
		changed := gx(x.Key, func(v Expr) { c.Key = v })
		changed = gxs(x.Keys, func(v []Expr) { c.Keys = v }) || changed
		changed = gx(x.LowKey, func(v Expr) { c.LowKey = v }) || changed
		changed = gx(x.HighKey, func(v Expr) { c.HighKey = v }) || changed
		changed = gx(x.Cond, func(v Expr) { c.Cond = v }) || changed
		if !changed {
			return n
		}
		return &c
	case *IndexOnlyScan:
		c := *x
		changed := gx(x.Key, func(v Expr) { c.Key = v })
		changed = gxs(x.Keys, func(v []Expr) { c.Keys = v }) || changed
		changed = gx(x.LowKey, func(v Expr) { c.LowKey = v }) || changed
		changed = gx(x.HighKey, func(v Expr) { c.HighKey = v }) || changed
		changed = gx(x.Cond, func(v Expr) { c.Cond = v }) || changed
		if !changed {
			return n
		}
		return &c
	case *BitmapHeapScan:
		c := *x
		changed := gx(x.Cond, func(v Expr) { c.Cond = v })
		changed = gxs(x.BitmapQual, func(v []Expr) { c.BitmapQual = v }) || changed
		if no := gc(x.Outer); no != x.Outer {
			c.Outer = no
			changed = true
		}
		if !changed {
			return n
		}
		return &c
	case *BitmapIndexScan:
		c := *x
		changed := gx(x.Key, func(v Expr) { c.Key = v })
		changed = gxs(x.Keys, func(v []Expr) { c.Keys = v }) || changed
		changed = gxs(x.Pred, func(v []Expr) { c.Pred = v }) || changed
		if !changed {
			return n
		}
		return &c
	case *Distinct:
		c := *x
		if nc := gc(x.Child); nc != x.Child {
			c.Child = nc
			return &c
		}
		return n
	case *WindowAgg:
		c := *x
		changed := gxs(x.PartitionBy, func(v []Expr) { c.PartitionBy = v })
		changed = gkeys(x.OrderBy, func(v []SortKey) { c.OrderBy = v }) || changed
		if len(x.Funcs) > 0 {
			fn := append([]WindowFunc(nil), x.Funcs...)
			for i, f := range fn {
				fch := false
				if len(f.Args) > 0 {
					aa := append([]Expr(nil), f.Args...)
					for j, e := range aa {
						if ne, ch := spliceField(e, st, s, anc); ch {
							aa[j] = ne
							fch = true
						}
					}
					if fch {
						fn[i].Args = aa
					}
				}
				if f.Filter != nil {
					if nf, ch := spliceField(f.Filter, st, s, anc); ch {
						fn[i].Filter = nf
						fch = true
					}
				}
				changed = changed || fch
			}
			if changed {
				c.Funcs = fn
			}
		}
		if nc := gc(x.Child); nc != x.Child {
			c.Child = nc
			changed = true
		}
		if !changed {
			return n
		}
		return &c
	case *Gather, *GatherMerge:
		// A Gather's partial subtree executes in workers: nested
		// sublinks stay serial (the nesting rule), but the shape
		// below is still traversed so shared-node memoing holds.
		var child Node
		if g, ok := n.(*Gather); ok {
			child = g.Child
		} else {
			child = n.(*GatherMerge).Child
		}
		if nc := graftNode(child, s, true, st); nc != child {
			if g, ok := n.(*Gather); ok {
				c := *g
				c.Child = nc
				return &c
			}
			c := *(n.(*GatherMerge))
			c.Child = nc
			return &c
		}
		return n
	case *LockRows:
		c := *x
		if nc := gc(x.Child); nc != x.Child {
			c.Child = nc
			return &c
		}
		return n
	}
	// Unmodelled node kinds (SetOp, CTE shapes, table functions,
	// DML, ...): fail closed — the subtree stays exactly as planned.
	return n
}

// graftTop runs placement discovery over root and grafts eligible
// sublink Plans. ancGathered skips everything: an ancestor already
// carries a Gather (the nesting rule).
func graftTop(root Node, s ParallelSettings, ancGathered bool) Node {
	if root == nil || ancGathered {
		return root
	}
	st := &sublinkGraftState{
		targets:    map[Expr]Node{},
		plans:      map[Node]Node{},
		nodes:      map[Node]Node{},
		unsafeSeen: map[Node]bool{},
	}
	WalkPlanExprs(root, func(e Expr) {
		if plan, ok := eligibleSublinkPlan(e); ok {
			st.targets[e] = plan
		}
	})
	if len(st.targets) == 0 {
		return root
	}
	return graftNode(root, s, false, st)
}
