package optimizer

// subplan_lower_walk.go — the sound traversal shared by the D4.1
// lowering pass's analysis and rewrite phases (subplan_lower.go).
//
// Soundness contract: analysis and rewrite use the SAME switch, so
// their reachability is identical by construction — a ref the analysis
// approved is a ref the rewrite reaches. Any node or expression kind
// this file does not model makes the traversal report !ok, and the
// enclosing sublink is left on the legacy stack path. Bailing on the
// unknown is the whole safety story: a lowered eval site stops pushing
// ctx.OuterRows, so a ref hiding in an unmodelled corner would dangle.

// lowerExprFn is applied to every expression node in pre-order. It
// returns (replacement, handled): handled=true installs the replacement
// and stops descent into that node; handled=false descends generically.
// ok=false anywhere aborts the whole traversal.
type lowerExprFn func(e Expr) (repl Expr, handled bool, ok bool)

// lowerTraverseNode walks a plan node's expressions and children,
// applying fx and writing back replacements. Returns false when an
// unmodelled node kind is encountered or fx reports failure.
func lowerTraverseNode(n Node, fx lowerExprFn) bool {
	if n == nil {
		return true
	}
	rewriteAll := func(es []Expr) bool {
		for i, e := range es {
			ne, ok := lowerTraverseExpr(e, fx)
			if !ok {
				return false
			}
			es[i] = ne
		}
		return true
	}
	one := func(p *Expr) bool {
		if *p == nil {
			return true
		}
		ne, ok := lowerTraverseExpr(*p, fx)
		if !ok {
			return false
		}
		*p = ne
		return true
	}
	switch x := n.(type) {
	case *SeqScan:
		return true
	case *IndexScan:
		// Cond is walked with the probe keys, not skipped: it holds the
		// relation's local quals when the scan is an NLI inner (plan.go,
		// IndexScan.Cond). Those quals reach a *Filter node on every other path
		// through the planner, and the *Filter arm below walks its Predicate —
		// so omitting Cond here would make the same predicate visible or
		// invisible to this pass depending on which join shape won, which is
		// exactly the sibling-divergence class of bug.
		if !one(&x.Key) || !one(&x.LowKey) || !one(&x.HighKey) || !one(&x.Cond) {
			return false
		}
		return rewriteAll(x.Keys)
	case *IndexOnlyScan:
		// Same for the IOS's residual qual. Today it is only ever a
		// `col IS NOT NULL` the min/max rewrite attached, so this is a no-op —
		// it is here so the two scan arms state the same rule.
		if !one(&x.Key) || !one(&x.LowKey) || !one(&x.HighKey) || !one(&x.Cond) {
			return false
		}
		return rewriteAll(x.Keys)
	case *BitmapHeapScan:
		// Same statement as the IndexScan arm's, one node family over:
		// `Cond` holds relation-local quals when the scan is an NLI inner,
		// `BitmapQual` is the recheck list, and the probe keys live on the
		// producer subtree under `Outer`. M0134-0185.
		if !one(&x.Cond) || !rewriteAll(x.BitmapQual) {
			return false
		}
		return lowerTraverseNode(x.Outer, fx)
	case *BitmapIndexScan:
		if !one(&x.Key) || !rewriteAll(x.Keys) {
			return false
		}
		return rewriteAll(x.Pred)
	case *BitmapAnd:
		for _, in := range x.Inputs {
			if !lowerTraverseNode(in, fx) {
				return false
			}
		}
		return true
	case *BitmapOr:
		for _, in := range x.Inputs {
			if !lowerTraverseNode(in, fx) {
				return false
			}
		}
		return true
	case *Filter:
		return one(&x.Predicate) && lowerTraverseNode(x.Child, fx)
	case *Project:
		return rewriteAll(x.Targets) && lowerTraverseNode(x.Child, fx)
	case *Aggregate:
		if !rewriteAll(x.GroupExprs) {
			return false
		}
		for i := range x.Aggs {
			a := &x.Aggs[i]
			if !one(&a.Arg) || !one(&a.Arg2) || !one(&a.Filter) {
				return false
			}
			if !rewriteAll(a.ExtraArgs) {
				return false
			}
			for j := range a.OrderBy {
				if !one(&a.OrderBy[j].Expr) {
					return false
				}
			}
			for j := range a.WithinGroupOrderBy {
				if !one(&a.WithinGroupOrderBy[j].Expr) {
					return false
				}
			}
		}
		return lowerTraverseNode(x.Child, fx)
	case *Sort:
		for i := range x.Keys {
			if !one(&x.Keys[i].Expr) {
				return false
			}
		}
		return lowerTraverseNode(x.Child, fx)
	case *Limit:
		return one(&x.Limit) && one(&x.Offset) && rewriteAll(x.TiesKeys) &&
			lowerTraverseNode(x.Child, fx)
	case *Distinct:
		return lowerTraverseNode(x.Child, fx)
	case *DistinctOn:
		// KeyCols are schema indices, not expressions.
		return lowerTraverseNode(x.Child, fx)
	case *Join:
		if !one(&x.Predicate) || !one(&x.LeftKey) || !one(&x.RightKey) {
			return false
		}
		return lowerTraverseNode(x.Left, fx) && lowerTraverseNode(x.Right, fx)
	case *NestedLoopIndexJoin:
		if !one(&x.Predicate) {
			return false
		}
		return lowerTraverseNode(x.Outer, fx) && lowerTraverseNode(x.Inner, fx)
	case *Values:
		for _, row := range x.Rows {
			if !rewriteAll(row) {
				return false
			}
		}
		return true
	case *OrdinalityWrap:
		// S4a scalar-residual rewrite wraps the outer side in an
		// ordinal tag; it carries no expressions of its own.
		return lowerTraverseNode(x.Child, fx)
	case *LockRows:
		if !one(&x.LimitCount) || !one(&x.OffsetCount) {
			return false
		}
		return lowerTraverseNode(x.Child, fx)
	}
	// Unmodelled node kind (WindowAgg, SRFs, CTE machinery, SetOp, ...):
	// bail — the sublink stays on the stack path.
	return false
}

// lowerTraverseExpr applies fx to e, then to its children when fx did
// not handle it. Unknown composite kinds bail; known leaves pass.
func lowerTraverseExpr(e Expr, fx lowerExprFn) (Expr, bool) {
	if e == nil {
		return nil, true
	}
	if repl, handled, ok := fx(e); !ok {
		return e, false
	} else if handled {
		return repl, true
	}
	one := func(p *Expr) bool {
		if *p == nil {
			return true
		}
		ne, ok := lowerTraverseExpr(*p, fx)
		if !ok {
			return false
		}
		*p = ne
		return true
	}
	all := func(es []Expr) bool {
		for i, c := range es {
			ne, ok := lowerTraverseExpr(c, fx)
			if !ok {
				return false
			}
			es[i] = ne
		}
		return true
	}
	switch x := e.(type) {
	// Leaves.
	case *IntegerConst, *NumericConst, *StringConst, *BooleanConst,
		*NullConst, *TypedStringLit, *IntervalLit, *ColumnRef,
		*OuterColumnRef, *ParamRef, *ExecParamRef, *CTIDExpr:
		return e, true
	case *BinaryOp:
		if !one(&x.Left) || !one(&x.Right) {
			return e, false
		}
		return e, true
	case *UnaryOp:
		return e, one(&x.Operand)
	case *CastExpr:
		return e, one(&x.Operand)
	case *FuncCall:
		return e, all(x.Args)
	case *ExtractExpr:
		return e, one(&x.Source)
	case *CollateExpr:
		return e, one(&x.Operand)
	case *IsNullExpr:
		return e, one(&x.Operand)
	case *IsBoolExpr:
		return e, one(&x.Operand)
	case *IsDistinctFromExpr:
		if !one(&x.Left) || !one(&x.Right) {
			return e, false
		}
		return e, true
	case *RowExpr:
		return e, all(x.Elems)
	case *CaseExpr:
		if !one(&x.Operand) || !one(&x.Else) {
			return e, false
		}
		for i := range x.Whens {
			if !one(&x.Whens[i].When) || !one(&x.Whens[i].Then) {
				return e, false
			}
		}
		return e, true
	case *InExpr:
		// Reached only for the literal-list form: sublink forms are
		// intercepted by the lowering callbacks before descent.
		if x.Plan != nil {
			return e, false
		}
		if !one(&x.Operand) {
			return e, false
		}
		return e, all(x.List)
	}
	// Unknown expression kind — could hide an OuterColumnRef; bail.
	return e, false
}
