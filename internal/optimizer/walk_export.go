package optimizer

// WalkPlanExprs invokes fn on every expression (recursively, via
// WalkExprTree) attached to every plan node walkPlanExprs models.
// Exported for the executor's SubPlan cacheability classifier
// (Stage 9 / D4.2): the walker is maintained here, next to the node
// definitions, so new node kinds extend exactly one enumeration.
// Note: fn does NOT descend into a sublink expression's inner Plan —
// callers that need that recurse explicitly on the sublink node.
func WalkPlanExprs(n Node, fn func(Expr)) { walkPlanExprs(n, fn) }

// WalkExprTree invokes fn on e and every sub-expression beneath it.
// Same sublink caveat as WalkPlanExprs.
func WalkExprTree(e Expr, fn func(Expr)) { walkExprTree(e, fn) }

// ExprSubplans returns the inner plan roots hung directly off one
// expression — the five F3 sublink kinds (A-01(ii) cut 2): scalar,
// ARRAY, IN, EXISTS (each via its Plan field) and the multi-assign
// row (directly, or via a MultiAssignSubqElem's shared Row). No
// recursion: callers walk each returned root themselves (NodeSubplans
// does one level per plan node; EXPLAIN's collect recurses through
// it).
//
// Built on exprChildSlots, not on a hand-written type switch: the
// sublink Plans are exactly the primitive's slotInnerPlan/slotSubqRow
// slots, so a future sublink kind taught to the primitive is picked up
// here automatically, while an unenumerated type contributes no bodies
// (fail-closed — silent unqualified rendering, never a wrong
// qualifier). This is what keeps the walker-inventory gate
// (exprwalk_inventory_test.go) from counting a new RC-1a site here.
func ExprSubplans(e Expr) []Node {
	slots, ok := exprChildSlots(e)
	if !ok {
		return nil
	}
	var out []Node
	for _, s := range slots {
		if s.kind == slotInnerPlan && s.plan != nil && *s.plan != nil {
			out = append(out, *s.plan)
		}
		if s.kind == slotSubqRow && s.row != nil && *s.row != nil && (*s.row).Plan != nil {
			out = append(out, (*s.row).Plan)
		}
	}
	return out
}

// NodeSubplans returns the subplan roots hung directly off n's OWN
// expressions (one level, no recursion into children or bodies).
// A-01(ii) cut 2 (F3): EXPLAIN's collect walker reaches Node children
// via planChildren but scalar/IN/EXISTS/ARRAY/multi-assign bodies hang
// off Expr fields, so without this no sublink-internal scan registers.
//
// The per-node expression list mirrors walkPlanExprs' arms (the
// maintained enumeration) plus the value-holders that walker never
// modeled: childless Result targets (the S6 min/max InitPlan hangs
// there), the DML nodes' SET/RETURNING/predicate expressions, the
// index-scan residual Conds, and LockRows' lifted limits. A kind
// missing here degrades silently to unqualified rendering for the
// sublinks it can hang — never to a wrong qualifier — so extend this
// switch when a node type gains expression fields.
func NodeSubplans(n Node) []Node {
	var exprs []Expr
	addKeys := func(keys []SortKey) {
		for _, k := range keys {
			if k.Expr != nil {
				exprs = append(exprs, k.Expr)
			}
		}
	}
	addAgg := func(a *AggregateCall) {
		if a == nil {
			return
		}
		if a.Arg != nil {
			exprs = append(exprs, a.Arg)
		}
		if a.Arg2 != nil {
			exprs = append(exprs, a.Arg2)
		}
		exprs = append(exprs, a.ExtraArgs...)
		if a.Filter != nil {
			exprs = append(exprs, a.Filter)
		}
		addKeys(a.OrderBy)
		addKeys(a.WithinGroupOrderBy)
	}
	switch t := n.(type) {
	case *Project:
		exprs = append(exprs, t.Targets...)
	case *Result:
		exprs = append(exprs, t.Targets...)
		if t.OneTimeFilter != nil {
			exprs = append(exprs, t.OneTimeFilter)
		}
	case *Filter:
		if t.Predicate != nil {
			exprs = append(exprs, t.Predicate)
		}
		exprs = append(exprs, t.PushedBelow...)
	case *Sort:
		addKeys(t.Keys)
	case *Limit:
		if t.Limit != nil {
			exprs = append(exprs, t.Limit)
		}
		if t.Offset != nil {
			exprs = append(exprs, t.Offset)
		}
		exprs = append(exprs, t.TiesKeys...)
	case *Join:
		if t.Predicate != nil {
			exprs = append(exprs, t.Predicate)
		}
		if t.LeftKey != nil {
			exprs = append(exprs, t.LeftKey)
		}
		if t.RightKey != nil {
			exprs = append(exprs, t.RightKey)
		}
	case *NestedLoopIndexJoin:
		if t.Predicate != nil {
			exprs = append(exprs, t.Predicate)
		}
	case *IndexScan:
		if t.Key != nil {
			exprs = append(exprs, t.Key)
		}
		exprs = append(exprs, t.Keys...)
		if t.LowKey != nil {
			exprs = append(exprs, t.LowKey)
		}
		if t.HighKey != nil {
			exprs = append(exprs, t.HighKey)
		}
		if t.Cond != nil {
			exprs = append(exprs, t.Cond)
		}
	case *IndexOnlyScan:
		if t.Key != nil {
			exprs = append(exprs, t.Key)
		}
		exprs = append(exprs, t.Keys...)
		if t.LowKey != nil {
			exprs = append(exprs, t.LowKey)
		}
		if t.HighKey != nil {
			exprs = append(exprs, t.HighKey)
		}
		if t.Cond != nil {
			exprs = append(exprs, t.Cond)
		}
	case *BitmapHeapScan:
		if t.Cond != nil {
			exprs = append(exprs, t.Cond)
		}
		exprs = append(exprs, t.BitmapQual...)
	case *BitmapIndexScan:
		if t.Key != nil {
			exprs = append(exprs, t.Key)
		}
		exprs = append(exprs, t.Keys...)
		exprs = append(exprs, t.Pred...)
	case *Aggregate:
		exprs = append(exprs, t.GroupExprs...)
		for i := range t.Aggs {
			addAgg(&t.Aggs[i])
		}
		exprs = append(exprs, t.Passthrough...)
	case *WindowAgg:
		exprs = append(exprs, t.PartitionBy...)
		addKeys(t.OrderBy)
		for i := range t.Funcs {
			exprs = append(exprs, t.Funcs[i].Args...)
			if t.Funcs[i].Filter != nil {
				exprs = append(exprs, t.Funcs[i].Filter)
			}
		}
		if t.Frame != nil {
			if t.Frame.StartOffset != nil {
				exprs = append(exprs, t.Frame.StartOffset)
			}
			if t.Frame.EndOffset != nil {
				exprs = append(exprs, t.Frame.EndOffset)
			}
		}
	case *Values:
		for _, row := range t.Rows {
			exprs = append(exprs, row...)
		}
	case *Update:
		exprs = append(exprs, t.Set...)
		exprs = append(exprs, t.Returning...)
		if t.FromPred != nil {
			exprs = append(exprs, t.FromPred)
		}
		if t.ViewCheckQual != nil {
			exprs = append(exprs, t.ViewCheckQual)
		}
	case *Delete:
		exprs = append(exprs, t.Returning...)
		if t.UsingPred != nil {
			exprs = append(exprs, t.UsingPred)
		}
	case *Merge:
		if t.On != nil {
			exprs = append(exprs, t.On)
		}
		exprs = append(exprs, t.Returning...)
		for _, c := range t.Clauses {
			if c == nil {
				continue
			}
			if c.Condition != nil {
				exprs = append(exprs, c.Condition)
			}
			exprs = append(exprs, c.UpdateSet...)
			exprs = append(exprs, c.InsertExprs...)
		}
	case *Insert:
		exprs = append(exprs, t.Returning...)
		if t.ViewCheckQual != nil {
			exprs = append(exprs, t.ViewCheckQual)
		}
		if t.OnConflict != nil {
			exprs = append(exprs, t.OnConflict.UpdateSet...)
			if t.OnConflict.UpdateWhere != nil {
				exprs = append(exprs, t.OnConflict.UpdateWhere)
			}
			exprs = append(exprs, t.OnConflict.ArbiterExprs...)
		}
	case *LockRows:
		if t.LimitCount != nil {
			exprs = append(exprs, t.LimitCount)
		}
		if t.OffsetCount != nil {
			exprs = append(exprs, t.OffsetCount)
		}
	case *ProjectSet:
		exprs = append(exprs, t.OtherExprs...)
		for _, uc := range t.UnnestCols {
			if uc.ArrExpr != nil {
				exprs = append(exprs, uc.ArrExpr)
			}
		}
	case *GenerateSeries:
		if t.Start != nil {
			exprs = append(exprs, t.Start)
		}
		if t.Stop != nil {
			exprs = append(exprs, t.Stop)
		}
		if t.Step != nil {
			exprs = append(exprs, t.Step)
		}
	case *GenerateSubscripts:
		if t.ArrExpr != nil {
			exprs = append(exprs, t.ArrExpr)
		}
		if t.Dim != nil {
			exprs = append(exprs, t.Dim)
		}
		if t.Reversed != nil {
			exprs = append(exprs, t.Reversed)
		}
	case *FromUnnest:
		if t.ArrExpr != nil {
			exprs = append(exprs, t.ArrExpr)
		}
	case *UserSrfScan:
		exprs = append(exprs, t.Args...)
	case *ScalarFuncScan:
		if t.Func != nil {
			exprs = append(exprs, t.Func)
		}
	case *PgGetPublicationTables:
		exprs = append(exprs, t.Args...)
	case *PgInputErrorInfo:
		if t.Value != nil {
			exprs = append(exprs, t.Value)
		}
		if t.Type != nil {
			exprs = append(exprs, t.Type)
		}
	case *PgGetSequenceData:
		exprs = append(exprs, t.Args...)
	case *PgOptionsToTable:
		if t.Arg != nil {
			exprs = append(exprs, t.Arg)
		}
	case *VerifyHeapam:
		if t.Arg != nil {
			exprs = append(exprs, t.Arg)
		}
		if t.StartBlock != nil {
			exprs = append(exprs, t.StartBlock)
		}
		if t.EndBlock != nil {
			exprs = append(exprs, t.EndBlock)
		}
	default:
		return nil
	}
	if len(exprs) == 0 {
		return nil
	}
	var out []Node
	seen := map[Node]struct{}{}
	for _, e := range exprs {
		if e == nil {
			continue
		}
		walkExprTree(e, func(sub Expr) {
			for _, sp := range ExprSubplans(sub) {
				if sp == nil {
					continue
				}
				if _, ok := seen[sp]; ok {
					continue
				}
				seen[sp] = struct{}{}
				out = append(out, sp)
			}
		})
	}
	return out
}
