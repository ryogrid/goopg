package planner

// unnestSubqueriesInPlan walks the plan tree and attempts to
// unnest any SubqueryExpr found in Filter predicates. This is
// the post-pass called after the initial plan tree is built
// and predicates have been pushed into joins.
func unnestSubqueriesInPlan(node Node) Node {
	if node == nil {
		return nil
	}
	switch n := node.(type) {
	case *Filter:
		n.Child = unnestSubqueriesInPlan(n.Child)
		for {
			sub := findSubqueryInExpr(n.Predicate)
			if sub == nil {
				break
			}
			newOuter, err := unnestSubquery(sub, node)
			if err != nil || newOuter == nil {
				break
			}
			node = newOuter
			if f, ok := newOuter.(*Filter); ok {
				n = f
			} else {
				return newOuter
			}
		}
		// M0040-0002: also try to unnest IN (subquery) expressions
		for {
			in := findInExprInExpr(n.Predicate)
			if in == nil {
				break
			}
			newOuter, err := unnestInExpr(in, node)
			if err != nil || newOuter == nil {
				break
			}
			node = newOuter
			if f, ok := newOuter.(*Filter); ok {
				n = f
			} else {
				return newOuter
			}
		}
		// M0061-0001: EXISTS / NOT EXISTS → semi-join / anti-join.
		// Same shape as the IN loop above; runs after IN-unnesting so
		// any IN inside an EXISTS subquery has already been pulled up.
		for {
			ex := findExistsExprInExpr(n.Predicate)
			if ex == nil {
				break
			}
			newOuter, err := unnestExistsExpr(ex, node)
			if err != nil || newOuter == nil {
				break
			}
			node = newOuter
			if f, ok := newOuter.(*Filter); ok {
				n = f
			} else {
				return newOuter
			}
		}
		// M0040-0004: walk remaining SubqueryExpr/InExpr inner plans
		// even when those expressions cannot be pulled up to this level
		// (e.g. Q20's lineitem scalar subquery inside the partsupp IN
		// clause, where the outer IN itself has no equijoin correlation).
		n.Predicate = walkSubqueryPlansInExpr(n.Predicate)
	case *Join:
		n.Left = unnestSubqueriesInPlan(n.Left)
		n.Right = unnestSubqueriesInPlan(n.Right)
	case *Project:
		n.Child = unnestSubqueriesInPlan(n.Child)
	case *Aggregate:
		n.Child = unnestSubqueriesInPlan(n.Child)
	case *Sort:
		n.Child = unnestSubqueriesInPlan(n.Child)
	case *Limit:
		n.Child = unnestSubqueriesInPlan(n.Child)
	}
	return node
}

// walkSubqueryPlansInExpr walks an expression tree and recursively
// applies unnestSubqueriesInPlan to the inner plan of every
// SubqueryExpr and InExpr node found. It is called after the
// pull-up loops in unnestSubqueriesInPlan so that subqueries that
// cannot be lifted to the current join level still have their own
// inner plan trees optimised.
func walkSubqueryPlansInExpr(e Expr) Expr {
	if e == nil {
		return nil
	}
	switch x := e.(type) {
	case *SubqueryExpr:
		x.Plan = unnestSubqueriesInPlan(x.Plan)
	case *InExpr:
		x.Plan = unnestSubqueriesInPlan(x.Plan)
	case *ExistsExpr:
		x.Plan = unnestSubqueriesInPlan(x.Plan)
	case *BinaryOp:
		x.Left = walkSubqueryPlansInExpr(x.Left)
		x.Right = walkSubqueryPlansInExpr(x.Right)
	case *UnaryOp:
		x.Operand = walkSubqueryPlansInExpr(x.Operand)
	case *FuncCall:
		for i := range x.Args {
			x.Args[i] = walkSubqueryPlansInExpr(x.Args[i])
		}
	case *CaseExpr:
		if x.Operand != nil {
			x.Operand = walkSubqueryPlansInExpr(x.Operand)
		}
		for i := range x.Whens {
			x.Whens[i].When = walkSubqueryPlansInExpr(x.Whens[i].When)
			x.Whens[i].Then = walkSubqueryPlansInExpr(x.Whens[i].Then)
		}
		x.Else = walkSubqueryPlansInExpr(x.Else)
	case *ExtractExpr:
		x.Source = walkSubqueryPlansInExpr(x.Source)
	}
	return e
}

func findSubqueryInExpr(e Expr) *SubqueryExpr {
	if e == nil {
		return nil
	}
	if s, ok := e.(*SubqueryExpr); ok {
		return s
	}
	switch x := e.(type) {
	case *BinaryOp:
		if s := findSubqueryInExpr(x.Left); s != nil {
			return s
		}
		return findSubqueryInExpr(x.Right)
	case *UnaryOp:
		return findSubqueryInExpr(x.Operand)
	case *FuncCall:
		for _, a := range x.Args {
			if s := findSubqueryInExpr(a); s != nil {
				return s
			}
		}
	case *CaseExpr:
		if x.Operand != nil {
			if s := findSubqueryInExpr(x.Operand); s != nil {
				return s
			}
		}
		for _, w := range x.Whens {
			if s := findSubqueryInExpr(w.When); s != nil {
				return s
			}
			if s := findSubqueryInExpr(w.Then); s != nil {
				return s
			}
		}
		if x.Else != nil {
			return findSubqueryInExpr(x.Else)
		}
	case *ExtractExpr:
		return findSubqueryInExpr(x.Source)
	}
	return nil
}

// canUnnestSubquery checks whether a SubqueryExpr is a candidate
// for unnesting into a GROUP BY aggregate + hash join.
func canUnnestSubquery(sub *SubqueryExpr) bool {
	plan := sub.Plan
	// Unwrap Project wrapper — the subquery's target list produces
	// a Project node wrapping the Aggregate.
	if proj, ok := plan.(*Project); ok {
		plan = proj.Child
	}
	agg, ok := plan.(*Aggregate)
	if !ok {
		return false
	}
	if len(agg.Aggs) != 1 {
		return false
	}
	call := agg.Aggs[0]
	if call.Star || call.Distinct {
		return false
	}
	params := collectUnnestParams(agg)
	if params == nil {
		return false
	}
	return len(params) > 0
}

func collectUnnestParams(node Node) []unnestParam {
	var params []unnestParam
	outerInEquijoin := make(map[*OuterColumnRef]bool)
	walkPlanExprs(node, func(e Expr) {
		bin, ok := e.(*BinaryOp)
		if !ok || bin.Op != "=" {
			return
		}
		outer, col := extractEquijoinPair(bin.Left, bin.Right)
		if outer != nil && col != nil {
			params = append(params, unnestParam{OuterRef: outer, SubCol: col})
			outerInEquijoin[outer] = true
		}
	})
	// Every OuterColumnRef in the plan must be accounted for by
	// an equijoin pair. If any OuterColumnRef appears outside an
	// equijoin, the subquery is not unnestable.
	allAccounted := true
	walkPlanExprs(node, func(e Expr) {
		if o, ok := e.(*OuterColumnRef); ok {
			if !outerInEquijoin[o] {
				allAccounted = false
			}
		}
	})
	if !allAccounted {
		return nil
	}
	return params
}

func extractEquijoinPair(a, b Expr) (*OuterColumnRef, *ColumnRef) {
	if o, ok := a.(*OuterColumnRef); ok {
		if c, ok := b.(*ColumnRef); ok {
			return o, c
		}
	}
	if o, ok := b.(*OuterColumnRef); ok {
		if c, ok := a.(*ColumnRef); ok {
			return o, c
		}
	}
	return nil, nil
}

func walkPlanExprs(node Node, visit func(Expr)) {
	if node == nil {
		return
	}
	switch n := node.(type) {
	case *Join:
		walkPlanExprs(n.Left, visit)
		walkPlanExprs(n.Right, visit)
		if n.Predicate != nil {
			walkExprTree(n.Predicate, visit)
		}
		if n.LeftKey != nil {
			walkExprTree(n.LeftKey, visit)
		}
		if n.RightKey != nil {
			walkExprTree(n.RightKey, visit)
		}
	case *Filter:
		walkPlanExprs(n.Child, visit)
		if n.Predicate != nil {
			walkExprTree(n.Predicate, visit)
		}
	case *Project:
		walkPlanExprs(n.Child, visit)
		for _, t := range n.Targets {
			walkExprTree(t, visit)
		}
	case *Aggregate:
		walkPlanExprs(n.Child, visit)
		for _, g := range n.GroupExprs {
			walkExprTree(g, visit)
		}
		for _, a := range n.Aggs {
			if a.Arg != nil {
				walkExprTree(a.Arg, visit)
			}
		}
	case *Sort:
		walkPlanExprs(n.Child, visit)
		for _, k := range n.Keys {
			walkExprTree(k.Expr, visit)
		}
	case *Limit:
		walkPlanExprs(n.Child, visit)
		if n.Limit != nil {
			walkExprTree(n.Limit, visit)
		}
		if n.Offset != nil {
			walkExprTree(n.Offset, visit)
		}
	case *SeqScan:
	case *IndexScan:
		if n.Key != nil {
			walkExprTree(n.Key, visit)
		}
		if n.LowKey != nil {
			walkExprTree(n.LowKey, visit)
		}
		if n.HighKey != nil {
			walkExprTree(n.HighKey, visit)
		}
	case *WindowAgg:
		walkPlanExprs(n.Child, visit)
		for _, p := range n.PartitionBy {
			walkExprTree(p, visit)
		}
		for _, k := range n.OrderBy {
			walkExprTree(k.Expr, visit)
		}
	case *Values:
		for _, row := range n.Rows {
			for _, e := range row {
				walkExprTree(e, visit)
			}
		}
	case *MultiHashJoin:
		for _, tbl := range n.Tables {
			walkPlanExprs(tbl, visit)
		}
		for _, f := range n.Filters {
			walkExprTree(f, visit)
		}
	}
}

// walkExprTree visits every expression node in a tree, calling
// visit for each. Named walkExprTree to avoid collision with
// the parser-level walkExpr in planner.go.
func walkExprTree(e Expr, visit func(Expr)) {
	if e == nil {
		return
	}
	visit(e)
	switch x := e.(type) {
	case *BinaryOp:
		walkExprTree(x.Left, visit)
		walkExprTree(x.Right, visit)
	case *UnaryOp:
		walkExprTree(x.Operand, visit)
	case *FuncCall:
		for _, a := range x.Args {
			walkExprTree(a, visit)
		}
	case *CaseExpr:
		walkExprTree(x.Operand, visit)
		for _, w := range x.Whens {
			walkExprTree(w.When, visit)
			walkExprTree(w.Then, visit)
		}
		walkExprTree(x.Else, visit)
	case *ExtractExpr:
		walkExprTree(x.Source, visit)
	}
}

func buildUnnestedSubquery(sub *SubqueryExpr, params []unnestParam) (Node, Schema, error) {
	plan := sub.Plan
	if proj, ok := plan.(*Project); ok {
		plan = proj.Child
	}
	agg, ok := plan.(*Aggregate)
	if !ok {
		return nil, nil, &PlanError{Pos: sub.Pos(), Code: "XX000", Message: "buildUnnestedSubquery: inner plan is not an Aggregate"}
	}
	replace := make(map[*OuterColumnRef]*ColumnRef, len(params))
	groupExprs := make([]Expr, len(params))
	for i, p := range params {
		replace[p.OuterRef] = p.SubCol
		groupExprs[i] = cloneExprLeaf(p.SubCol)
	}
	child, err := clonePlanReplacingOuter(agg.Child, replace)
	if err != nil {
		return nil, nil, err
	}
	newAgg := &Aggregate{
		pos:        agg.Pos(),
		Child:      child,
		GroupExprs: groupExprs,
		Aggs:       []AggregateCall{cloneAggregateCall(agg.Aggs[0])},
	}
	schema := make(Schema, 0, len(params)+1)
	for _, p := range params {
		schema = append(schema, SchemaColumn{Name: p.SubCol.Name, Type: p.SubCol.Type})
	}
	schema = append(schema, SchemaColumn{
		Name: agg.Aggs[0].Name,
		Type: agg.Aggs[0].Type,
	})
	newAgg.schema = schema
	return newAgg, schema, nil
}

func clonePlanReplacingOuter(node Node, replace map[*OuterColumnRef]*ColumnRef) (Node, error) {
	if node == nil {
		return nil, nil
	}
	switch n := node.(type) {
	case *Join:
		left, err := clonePlanReplacingOuter(n.Left, replace)
		if err != nil {
			return nil, err
		}
		right, err := clonePlanReplacingOuter(n.Right, replace)
		if err != nil {
			return nil, err
		}
		jn := *n
		jn.Left = left
		jn.Right = right
		if n.Predicate != nil {
			jn.Predicate = cloneExprReplacingOuter(n.Predicate, replace)
		}
		if n.LeftKey != nil {
			jn.LeftKey = cloneExprReplacingOuter(n.LeftKey, replace)
		}
		if n.RightKey != nil {
			jn.RightKey = cloneExprReplacingOuter(n.RightKey, replace)
		}
		return &jn, nil
	case *Filter:
		child, err := clonePlanReplacingOuter(n.Child, replace)
		if err != nil {
			return nil, err
		}
		f := *n
		f.Child = child
		if n.Predicate != nil {
			f.Predicate = cloneExprReplacingOuter(n.Predicate, replace)
		}
		return &f, nil
	case *Project:
		child, err := clonePlanReplacingOuter(n.Child, replace)
		if err != nil {
			return nil, err
		}
		pr := *n
		pr.Child = child
		pr.Targets = make([]Expr, len(n.Targets))
		for i, t := range n.Targets {
			pr.Targets[i] = cloneExprReplacingOuter(t, replace)
		}
		return &pr, nil
	case *Aggregate:
		child, err := clonePlanReplacingOuter(n.Child, replace)
		if err != nil {
			return nil, err
		}
		a := *n
		a.Child = child
		a.GroupExprs = make([]Expr, len(n.GroupExprs))
		for i, g := range n.GroupExprs {
			a.GroupExprs[i] = cloneExprReplacingOuter(g, replace)
		}
		a.Aggs = make([]AggregateCall, len(n.Aggs))
		for i, ag := range n.Aggs {
			a.Aggs[i] = ag
			if ag.Arg != nil {
				a.Aggs[i].Arg = cloneExprReplacingOuter(ag.Arg, replace)
			}
		}
		return &a, nil
	case *Sort:
		child, err := clonePlanReplacingOuter(n.Child, replace)
		if err != nil {
			return nil, err
		}
		s := *n
		s.Child = child
		s.Keys = make([]SortKey, len(n.Keys))
		for i, k := range n.Keys {
			s.Keys[i] = SortKey{Expr: cloneExprReplacingOuter(k.Expr, replace), Desc: k.Desc}
		}
		return &s, nil
	case *Limit:
		child, err := clonePlanReplacingOuter(n.Child, replace)
		if err != nil {
			return nil, err
		}
		l := *n
		l.Child = child
		if n.Limit != nil {
			l.Limit = cloneExprReplacingOuter(n.Limit, replace)
		}
		if n.Offset != nil {
			l.Offset = cloneExprReplacingOuter(n.Offset, replace)
		}
		return &l, nil
	case *SeqScan:
		c := *n
		return &c, nil
	case *IndexScan:
		c := *n
		if n.Key != nil {
			c.Key = cloneExprReplacingOuter(n.Key, replace)
		}
		if n.LowKey != nil {
			c.LowKey = cloneExprReplacingOuter(n.LowKey, replace)
		}
		if n.HighKey != nil {
			c.HighKey = cloneExprReplacingOuter(n.HighKey, replace)
		}
		return &c, nil
	case *MultiHashJoin:
		c := *n
		c.Tables = make([]Node, len(n.Tables))
		for i, tbl := range n.Tables {
			cloned, err := clonePlanReplacingOuter(tbl, replace)
			if err != nil {
				return nil, err
			}
			c.Tables[i] = cloned
		}
		c.Filters = nil
		if n.Filters != nil {
			c.Filters = make([]Expr, len(n.Filters))
			for i, f := range n.Filters {
				c.Filters[i] = cloneExprReplacingOuter(f, replace)
			}
		}
		c.Keys = make([]MultiHashKey, len(n.Keys))
		for i, k := range n.Keys {
			c.Keys[i] = k
		}
		return &c, nil
	case *Values:
		c := *n
		c.Rows = make([][]Expr, len(n.Rows))
		for i, row := range n.Rows {
			c.Rows[i] = make([]Expr, len(row))
			for j, e := range row {
				c.Rows[i][j] = cloneExprReplacingOuter(e, replace)
			}
		}
		return &c, nil
	default:
		return nil, &PlanError{Pos: node.Pos(), Code: "XX000", Message: "clonePlanReplacingOuter: unsupported plan node"}
	}
}

func cloneExprReplacingOuter(e Expr, replace map[*OuterColumnRef]*ColumnRef) Expr {
	if e == nil {
		return nil
	}
	if o, ok := e.(*OuterColumnRef); ok {
		if c, found := replace[o]; found {
			cl := *c
			return &cl
		}
		cl := *o
		return &cl
	}
	switch x := e.(type) {
	case *ColumnRef:
		cl := *x
		return &cl
	case *BinaryOp:
		return &BinaryOp{
			pos:   x.Pos(),
			Op:    x.Op,
			Left:  cloneExprReplacingOuter(x.Left, replace),
			Right: cloneExprReplacingOuter(x.Right, replace),
		}
	case *UnaryOp:
		return &UnaryOp{
			pos:     x.Pos(),
			Op:      x.Op,
			Operand: cloneExprReplacingOuter(x.Operand, replace),
		}
	case *FuncCall:
		cl := *x
		cl.Args = make([]Expr, len(x.Args))
		for i, a := range x.Args {
			cl.Args[i] = cloneExprReplacingOuter(a, replace)
		}
		return &cl
	case *CaseExpr:
		cl := *x
		if x.Operand != nil {
			cl.Operand = cloneExprReplacingOuter(x.Operand, replace)
		}
		cl.Whens = make([]CaseWhen, len(x.Whens))
		for i, w := range x.Whens {
			cl.Whens[i] = CaseWhen{
				When: cloneExprReplacingOuter(w.When, replace),
				Then: cloneExprReplacingOuter(w.Then, replace),
			}
		}
		if x.Else != nil {
			cl.Else = cloneExprReplacingOuter(x.Else, replace)
		}
		return &cl
	case *ExtractExpr:
		cl := *x
		cl.Source = cloneExprReplacingOuter(x.Source, replace)
		return &cl
	default:
		return cloneExprLeaf(x)
	}
}

func cloneExprLeaf(e Expr) Expr {
	if e == nil {
		return nil
	}
	switch x := e.(type) {
	case *IntegerConst:
		c := *x
		return &c
	case *NumericConst:
		c := *x
		return &c
	case *StringConst:
		c := *x
		return &c
	case *NullConst:
		c := *x
		return &c
	case *BooleanConst:
		c := *x
		return &c
	case *ParamRef:
		c := *x
		return &c
	case *TypedStringLit:
		c := *x
		return &c
	case *IntervalLit:
		c := *x
		return &c
	case *ColumnRef:
		c := *x
		return &c
	case *OuterColumnRef:
		c := *x
		return &c
	default:
		return e
	}
}

func cloneAggregateCall(call AggregateCall) AggregateCall {
	c := call
	if call.Arg != nil {
		c.Arg = cloneExprLeaf(call.Arg)
	}
	return c
}

func findFilterContainingSubquery(node Node, target *SubqueryExpr) (*Filter, Expr) {
	if node == nil {
		return nil, nil
	}
	switch n := node.(type) {
	case *Filter:
		conjuncts := splitAnd(n.Predicate)
		for _, c := range conjuncts {
			if containsExpr(c, target) {
				return n, c
			}
		}
		return findFilterContainingSubquery(n.Child, target)
	case *Join:
		if f, c := findFilterContainingSubquery(n.Left, target); f != nil {
			return f, c
		}
		return findFilterContainingSubquery(n.Right, target)
	case *Project:
		return findFilterContainingSubquery(n.Child, target)
	case *Aggregate:
		return findFilterContainingSubquery(n.Child, target)
	case *Sort:
		return findFilterContainingSubquery(n.Child, target)
	case *Limit:
		return findFilterContainingSubquery(n.Child, target)
	case *MultiHashJoin:
		for _, tbl := range n.Tables {
			if f, c := findFilterContainingSubquery(tbl, target); f != nil {
				return f, c
			}
		}
	}
	return nil, nil
}

func containsExpr(e, target Expr) bool {
	if e == nil {
		return false
	}
	if e == target {
		return true
	}
	switch x := e.(type) {
	case *BinaryOp:
		return containsExpr(x.Left, target) || containsExpr(x.Right, target)
	case *UnaryOp:
		return containsExpr(x.Operand, target)
	case *FuncCall:
		for _, a := range x.Args {
			if containsExpr(a, target) {
				return true
			}
		}
	case *CaseExpr:
		if x.Operand != nil && containsExpr(x.Operand, target) {
			return true
		}
		for _, w := range x.Whens {
			if containsExpr(w.When, target) || containsExpr(w.Then, target) {
				return true
			}
		}
		if x.Else != nil && containsExpr(x.Else, target) {
			return true
		}
	case *ExtractExpr:
		return containsExpr(x.Source, target)
	}
	return false
}

func replaceExprInConjunct(e, target, replacement Expr) Expr {
	if e == target {
		return replacement
	}
	switch x := e.(type) {
	case *BinaryOp:
		return &BinaryOp{
			pos:   x.Pos(),
			Op:    x.Op,
			Left:  replaceExprInConjunct(x.Left, target, replacement),
			Right: replaceExprInConjunct(x.Right, target, replacement),
		}
	case *UnaryOp:
		return &UnaryOp{
			pos:     x.Pos(),
			Op:      x.Op,
			Operand: replaceExprInConjunct(x.Operand, target, replacement),
		}
	case *FuncCall:
		cl := *x
		cl.Args = make([]Expr, len(x.Args))
		for i, a := range x.Args {
			cl.Args[i] = replaceExprInConjunct(a, target, replacement)
		}
		return &cl
	case *CaseExpr:
		cl := *x
		if x.Operand != nil {
			cl.Operand = replaceExprInConjunct(x.Operand, target, replacement)
		}
		cl.Whens = make([]CaseWhen, len(x.Whens))
		for i, w := range x.Whens {
			cl.Whens[i] = CaseWhen{
				When: replaceExprInConjunct(w.When, target, replacement),
				Then: replaceExprInConjunct(w.Then, target, replacement),
			}
		}
		if x.Else != nil {
			cl.Else = replaceExprInConjunct(x.Else, target, replacement)
		}
		return &cl
	case *ExtractExpr:
		cl := *x
		cl.Source = replaceExprInConjunct(x.Source, target, replacement)
		return &cl
	}
	return e
}

// unnestSubquery attempts to unnest a SubqueryExpr from an outer
// Filter. Returns a new outer plan tree or nil if unnesting fails.
func unnestSubquery(sub *SubqueryExpr, outer Node) (Node, error) {
	if !canUnnestSubquery(sub) {
		return nil, nil
	}
	plan := sub.Plan
	if proj, ok := plan.(*Project); ok {
		plan = proj.Child
	}
	params := collectUnnestParams(plan.(*Aggregate))
	if len(params) == 0 {
		return nil, nil
	}
	subPlan, subSchema, err := buildUnnestedSubquery(sub, params)
	if err != nil {
		return nil, err
	}
	filter, conjunct := findFilterContainingSubquery(outer, sub)
	if filter == nil {
		return nil, nil
	}
	outerChild := filter.Child
	outerWidth := len(outerChild.Output())
	aggColRef := &ColumnRef{
		pos:   sub.Pos(),
		Index: outerWidth + len(params),
		Name:  subSchema[len(params)].Name,
		Type:  subSchema[len(params)].Type,
	}
	newConjunct := replaceExprInConjunct(conjunct, sub, aggColRef)
	conjuncts := splitAnd(filter.Predicate)
	newConjuncts := make([]Expr, 0, len(conjuncts))
	for _, c := range conjuncts {
		if c == conjunct {
			newConjuncts = append(newConjuncts, newConjunct)
		} else {
			newConjuncts = append(newConjuncts, c)
		}
	}
	filter.Predicate = combineAnd(newConjuncts)

	// M0054-0008: handle multi-parameter correlation. The inner
	// Aggregate now groups by every correlation column (built by
	// buildUnnestedSubquery), and the outer join needs to bind
	// every (outer-col, inner-col) pair. The first pair becomes
	// the hash key (LeftKey/RightKey); additional pairs go into
	// the join Predicate as AND-conjuncts so the hash-join's
	// post-match evaluation filters out rows that match the first
	// key but disagree on the remaining keys. Without this fix,
	// a Q20-shape subquery with `l_partkey = ps_partkey AND
	// l_suppkey = ps_suppkey` would match on l_partkey alone and
	// produce wrong sums.
	outerKeyExprs := make([]*ColumnRef, len(params))
	innerKeyExprs := make([]*ColumnRef, len(params))
	for i, p := range params {
		outerKeyExprs[i] = &ColumnRef{
			pos:   p.OuterRef.Pos(),
			Index: p.OuterRef.Index,
			Name:  p.OuterRef.Name,
			Type:  p.OuterRef.Type,
		}
		innerKeyExprs[i] = &ColumnRef{
			pos:   p.SubCol.Pos(),
			Index: outerWidth + i, // i-th group-by column in subSchema
			Name:  p.SubCol.Name,
			Type:  p.SubCol.Type,
		}
	}
	// Build the join Predicate as AND of all per-pair equalities.
	var joinPredicate Expr = &BinaryOp{
		pos: sub.Pos(), Op: "=",
		Left:  outerKeyExprs[0],
		Right: innerKeyExprs[0],
	}
	for i := 1; i < len(params); i++ {
		eq := &BinaryOp{
			pos: sub.Pos(), Op: "=",
			Left:  outerKeyExprs[i],
			Right: innerKeyExprs[i],
		}
		joinPredicate = &BinaryOp{pos: sub.Pos(), Op: "AND", Left: joinPredicate, Right: eq}
	}
	mergedSchema := make(Schema, outerWidth+len(subSchema))
	copy(mergedSchema, outerChild.Output())
	for i, sc := range subSchema {
		mergedSchema[outerWidth+i] = sc
	}
	join := &Join{
		pos:       sub.Pos(),
		Type:      JoinTypeInner,
		Algo:      JoinAlgoHash,
		Left:      outerChild,
		Right:     subPlan,
		Predicate: joinPredicate,
		// Hash key uses the FIRST param's pair. The remaining
		// per-pair equalities are enforced as residuals via the
		// Predicate above.
		LeftKey:  outerKeyExprs[0],
		RightKey: &ColumnRef{pos: sub.Pos(), Index: 0, Name: params[0].SubCol.Name, Type: params[0].SubCol.Type},
		schema:   mergedSchema,
	}
	filter.Child = join
	return outer, nil
}

// --- M0040-0002: IN (subquery) → semi-join unnest ---

// findInExprInExpr walks an expression tree looking for an
// InExpr node whose source is a subquery (Plan != nil).
func findInExprInExpr(e Expr) *InExpr {
	if e == nil {
		return nil
	}
	if in, ok := e.(*InExpr); ok && in.Plan != nil {
		return in
	}
	switch x := e.(type) {
	case *BinaryOp:
		if s := findInExprInExpr(x.Left); s != nil {
			return s
		}
		return findInExprInExpr(x.Right)
	case *UnaryOp:
		return findInExprInExpr(x.Operand)
	case *FuncCall:
		for _, a := range x.Args {
			if s := findInExprInExpr(a); s != nil {
				return s
			}
		}
	case *CaseExpr:
		if x.Operand != nil {
			if s := findInExprInExpr(x.Operand); s != nil {
				return s
			}
		}
		for _, w := range x.Whens {
			if s := findInExprInExpr(w.When); s != nil {
				return s
			}
			if s := findInExprInExpr(w.Then); s != nil {
				return s
			}
		}
		if x.Else != nil {
			return findInExprInExpr(x.Else)
		}
	case *ExtractExpr:
		return findInExprInExpr(x.Source)
	}
	return nil
}

// canUnnestInExpr checks whether an IN (subquery) is a candidate
// for unnesting into a semi-join.  The inner plan must have a
// correlation predicate of the shape `inner_col = outer_ref`
// (every OuterColumnRef participates in an equijoin pair).
//
// M0062-0004: nested IN subqueries are accepted **iff** every
// nested IN is itself unnestable. This unblocks Q20 (depth-2 IN
// chain). The recursion cap prevents pathological nesting.
func canUnnestInExpr(in *InExpr) bool {
	return canUnnestInExprDepth(in, 0)
}

// canUnnestInExprDepth is the recursive worker for
// canUnnestInExpr. The depth cap is intentionally low: real TPC-H
// queries don't nest beyond 2 levels, and an unbounded recursion
// would let a hostile or pathologically nested predicate run a
// quadratic walkPlanExprs scan per level.
const maxNestedInUnnestDepth = 4

func canUnnestInExprDepth(in *InExpr, depth int) bool {
	if depth > maxNestedInUnnestDepth {
		return false
	}
	plan := in.Plan
	if plan == nil {
		return false
	}
	// Collect equijoin pairs — all OuterColumnRefs must be in
	// equality joins with inner ColumnRefs.
	params := collectUnnestParams(plan)
	if params == nil || len(params) == 0 {
		return false
	}
	// M0062-0004: nested IN subqueries are now accepted *if* each
	// is itself unnestable. The pre-fix blanket reject blocked
	// Q20's depth-2 IN. The recursive `unnestSubqueriesInPlan`
	// call inside `unnestInExpr` (at the inner-plan-clone step)
	// will lift the inner IN up first; this gate just admits the
	// outer.
	allNestedUnnestable := true
	walkPlanExprs(plan, func(e Expr) {
		if in2, ok := e.(*InExpr); ok && in2.Plan != nil {
			if !canUnnestInExprDepth(in2, depth+1) {
				allNestedUnnestable = false
			}
		}
	})
	if !allNestedUnnestable {
		return false
	}
	return true
}

// unnestInExpr rewrites an IN (subquery) as a semi-join.
//
//	Filter(column IN (SELECT inner_col FROM ... WHERE inner_col = outer.col), outer)
//	→  JoinTypeSemi(outer, inner_plan_clone)
//
// The inner plan is cloned with OuterColumnRef → ColumnRef
// replacement so it no longer depends on the outer scope.
func unnestInExpr(in *InExpr, outer Node) (Node, error) {
	if !canUnnestInExpr(in) {
		return nil, nil
	}
	params := collectUnnestParams(in.Plan)
	if len(params) == 0 {
		return nil, nil
	}
	// Replace OuterColumnRefs in the inner plan with their
	// corresponding ColumnRefs so the inner plan is self-contained.
	replace := make(map[*OuterColumnRef]*ColumnRef, len(params))
	for _, p := range params {
		replace[p.OuterRef] = p.SubCol
	}
	innerPlan, err := clonePlanReplacingOuter(in.Plan, replace)
	if err != nil {
		return nil, err
	}
	// Recursively unnest any scalar subqueries still inside the
	// inner plan (e.g. Q20's lineitem aggregate inside the
	// partsupp IN subquery).
	innerPlan = unnestSubqueriesInPlan(innerPlan)

	// Find the Filter that wraps the outer node.
	filter, conjunct := findFilterContainingInExpr(outer, in)
	if filter == nil {
		return nil, nil
	}

	outerChild := filter.Child
	outerWidth := len(outerChild.Output())
	innerWidth := len(innerPlan.Output())

	// Build semi-join keys.
	outerKey := &ColumnRef{
		pos:   params[0].OuterRef.Pos(),
		Index: params[0].OuterRef.Index,
		Name:  params[0].OuterRef.Name,
		Type:  params[0].OuterRef.Type,
	}
	innerKey := &ColumnRef{
		pos:   params[0].SubCol.Pos(),
		Index: outerWidth,
		Name:  params[0].SubCol.Name,
		Type:  params[0].SubCol.Type,
	}

	// Build a semi-join predicate that replaces the IN expression
	// in the filter.  `column = inner_col` (single param).
	semiPred := &BinaryOp{pos: in.Pos(), Op: "=", Left: outerKey, Right: innerKey}

	// Remove the IN conjunct from the filter and add the
	// semi-join predicate.
	conjuncts := splitAnd(filter.Predicate)
	newConjuncts := make([]Expr, 0, len(conjuncts))
	found := false
	for _, c := range conjuncts {
		if c == conjunct {
			if !found {
				newConjuncts = append(newConjuncts, semiPred)
				found = true
			}
			// skip the original IN conjunct
		} else {
			newConjuncts = append(newConjuncts, c)
		}
	}
	if !found {
		newConjuncts = append(newConjuncts, semiPred)
	}
	filter.Predicate = combineAnd(newConjuncts)

	mergedSchema := make(Schema, outerWidth+innerWidth)
	copy(mergedSchema, outerChild.Output())
	copy(mergedSchema[outerWidth:], innerPlan.Output())

	// Use inner join with dedup on the right side (semi-join)
	// — JoinTypeSemi builds a deduplicated set of the right
	// child and probes from the left.
	join := &Join{
		pos:       in.Pos(),
		Type:      JoinTypeInner,
		Algo:      JoinAlgoHash,
		Left:      outerChild,
		Right:     innerPlan,
		Predicate: semiPred,
		LeftKey:   outerKey,
		RightKey:  innerKey,
		schema:    mergedSchema,
	}
	filter.Child = join
	return outer, nil
}

// findFilterContainingInExpr walks the plan tree looking for the
// Filter node that wraps inner containing the given conjunct
// expression (the IN expression).
func findFilterContainingInExpr(node Node, target *InExpr) (*Filter, Expr) {
	if node == nil {
		return nil, nil
	}
	if f, ok := node.(*Filter); ok {
		if c := findExprInExpr(f.Predicate, func(e Expr) bool {
			return e == target
		}); c != nil {
			return f, c
		}
	}
	switch n := node.(type) {
	case *Join:
		if f, c := findFilterContainingInExpr(n.Left, target); f != nil {
			return f, c
		}
		return findFilterContainingInExpr(n.Right, target)
	case *Filter:
		return findFilterContainingInExpr(n.Child, target)
	case *Project:
		return findFilterContainingInExpr(n.Child, target)
	case *Aggregate:
		return findFilterContainingInExpr(n.Child, target)
	case *Sort:
		return findFilterContainingInExpr(n.Child, target)
	case *Limit:
		return findFilterContainingInExpr(n.Child, target)
	case *MultiHashJoin:
		for _, tbl := range n.Tables {
			if f, c := findFilterContainingInExpr(tbl, target); f != nil {
				return f, c
			}
		}
	}
	return nil, nil
}

// findExprInExpr returns the first non-nil expression in the
// tree for which match returns true.  Returns nil if no match.
func findExprInExpr(e Expr, match func(Expr) bool) Expr {
	if e == nil {
		return nil
	}
	if match(e) {
		return e
	}
	switch x := e.(type) {
	case *BinaryOp:
		if r := findExprInExpr(x.Left, match); r != nil {
			return r
		}
		return findExprInExpr(x.Right, match)
	case *UnaryOp:
		return findExprInExpr(x.Operand, match)
	case *FuncCall:
		for _, a := range x.Args {
			if r := findExprInExpr(a, match); r != nil {
				return r
			}
		}
	}
	return nil
}

// --- M0061-0001: EXISTS / NOT EXISTS → semi-join / anti-join ---

// findExistsExprInExpr walks an expression tree looking for an
// ExistsExpr that lives at a top-level conjunct position. Like the
// IN-unnesting pass we reuse the structural walker; the
// canUnnestExistsExpr gate then checks that the EXISTS conjunct can
// be safely lifted (correlated equijoin only).
func findExistsExprInExpr(e Expr) *ExistsExpr {
	if e == nil {
		return nil
	}
	if ex, ok := e.(*ExistsExpr); ok && ex.Plan != nil {
		return ex
	}
	switch x := e.(type) {
	case *BinaryOp:
		if s := findExistsExprInExpr(x.Left); s != nil {
			return s
		}
		return findExistsExprInExpr(x.Right)
	case *UnaryOp:
		return findExistsExprInExpr(x.Operand)
	case *FuncCall:
		for _, a := range x.Args {
			if s := findExistsExprInExpr(a); s != nil {
				return s
			}
		}
	case *CaseExpr:
		if x.Operand != nil {
			if s := findExistsExprInExpr(x.Operand); s != nil {
				return s
			}
		}
		for _, w := range x.Whens {
			if s := findExistsExprInExpr(w.When); s != nil {
				return s
			}
			if s := findExistsExprInExpr(w.Then); s != nil {
				return s
			}
		}
		if x.Else != nil {
			return findExistsExprInExpr(x.Else)
		}
	case *ExtractExpr:
		return findExistsExprInExpr(x.Source)
	}
	return nil
}

// stripOuterRefConjuncts walks `node` and removes from every
// Filter's Predicate any top-level conjunct that still references
// an OuterColumnRef. Used by `unnestExistsExpr` (M0062-0005)
// after cloning the inner plan: the residual conjuncts we lifted
// to the join's Predicate must not also be evaluated by the
// inner Filter (they reference unbound OuterColumnRefs at the
// inner plan's scope). Conjuncts that became self-equality
// tautologies via the equijoin `replace` map are also dropped.
func stripOuterRefConjuncts(node Node) {
	if node == nil {
		return
	}
	switch n := node.(type) {
	case *Filter:
		conjs := splitAnd(n.Predicate)
		out := make([]Expr, 0, len(conjs))
		for _, c := range conjs {
			hasOuter := false
			tautology := false
			walkExprTree(c, func(e Expr) {
				if _, ok := e.(*OuterColumnRef); ok {
					hasOuter = true
				}
			})
			if bin, ok := c.(*BinaryOp); ok && bin.Op == "=" {
				if l, lok := bin.Left.(*ColumnRef); lok {
					if r, rok := bin.Right.(*ColumnRef); rok {
						if l.Index == r.Index && l.Name == r.Name {
							tautology = true
						}
					}
				}
			}
			if hasOuter || tautology {
				continue
			}
			out = append(out, c)
		}
		if len(out) == 0 {
			n.Predicate = &BooleanConst{pos: n.pos, Value: true}
		} else {
			n.Predicate = combineAnd(out)
		}
		stripOuterRefConjuncts(n.Child)
	case *Project:
		stripOuterRefConjuncts(n.Child)
	case *Aggregate:
		stripOuterRefConjuncts(n.Child)
	case *Sort:
		stripOuterRefConjuncts(n.Child)
	case *Limit:
		stripOuterRefConjuncts(n.Child)
	case *Join:
		stripOuterRefConjuncts(n.Left)
		stripOuterRefConjuncts(n.Right)
	}
}

// existsUnnestPlan describes how an EXISTS subquery's correlation
// predicate decomposes for unnesting:
//   - Params: equijoin pairs that become the join's hash key(s).
//   - Residuals: the original (uncloned) BinaryOp / general
//     conjunct expressions that reference an OuterColumnRef but
//     are NOT a pure column-equijoin pair. Examples: `<>`,
//     range comparisons, function calls. After unnesting these
//     are lifted onto the join's `Predicate` with both inner
//     ColumnRefs shifted by `outerWidth` and OuterColumnRefs
//     rewritten to ColumnRefs at their outer-side index.
//
// (M0062-0005.)
type existsUnnestPlan struct {
	Params    []unnestParam
	Residuals []Expr
}

// collectExistsUnnestParamsAndResiduals splits the inner plan's
// top-level conjuncts (anywhere a Filter sits) into:
//   - equijoin params (existing collectUnnestParams logic), and
//   - non-equi residuals that reference an OuterColumnRef.
//
// Returns nil if the plan has any OuterColumnRef that does NOT
// appear in either bucket — those would still be correlated after
// the lift, which we cannot handle.
func collectExistsUnnestParamsAndResiduals(node Node) *existsUnnestPlan {
	var params []unnestParam
	var residuals []Expr
	outerInEquijoin := make(map[*OuterColumnRef]bool)
	outerInResidual := make(map[*OuterColumnRef]bool)

	// Walk every Filter in the inner plan; only top-level
	// conjuncts of a Filter's Predicate may be lifted (an
	// OuterColumnRef nested inside an OR or a function call
	// can't be cleanly removed from the inner plan).
	var walkFilters func(Node)
	walkFilters = func(n Node) {
		if n == nil {
			return
		}
		switch x := n.(type) {
		case *Filter:
			walkFilters(x.Child)
			for _, c := range splitAnd(x.Predicate) {
				bin, ok := c.(*BinaryOp)
				if ok && bin.Op == "=" {
					outer, col := extractEquijoinPair(bin.Left, bin.Right)
					if outer != nil && col != nil {
						params = append(params, unnestParam{OuterRef: outer, SubCol: col})
						outerInEquijoin[outer] = true
						continue
					}
				}
				// Non-equijoin conjunct. If it mentions an
				// OuterColumnRef and ONLY has top-level outer
				// refs (no OuterColumnRef nested under e.g. a
				// further OR or CASE), record it as a residual.
				var refs []*OuterColumnRef
				outerOK := true
				walkExprTree(c, func(e Expr) {
					if oc, ok := e.(*OuterColumnRef); ok {
						refs = append(refs, oc)
					}
				})
				_ = outerOK
				if len(refs) > 0 {
					residuals = append(residuals, c)
					for _, oc := range refs {
						outerInResidual[oc] = true
					}
				}
			}
		case *Project:
			walkFilters(x.Child)
		case *Aggregate:
			walkFilters(x.Child)
		case *Sort:
			walkFilters(x.Child)
		case *Limit:
			walkFilters(x.Child)
		case *Join:
			walkFilters(x.Left)
			walkFilters(x.Right)
		}
	}
	walkFilters(node)

	// Verify every OuterColumnRef is accounted for.
	allAccounted := true
	walkPlanExprs(node, func(e Expr) {
		if o, ok := e.(*OuterColumnRef); ok {
			if !outerInEquijoin[o] && !outerInResidual[o] {
				allAccounted = false
			}
		}
	})
	if !allAccounted {
		return nil
	}
	return &existsUnnestPlan{Params: params, Residuals: residuals}
}

// canUnnestExistsExpr accepts a correlated EXISTS subquery whose
// correlation predicate can be split into equijoin keys plus
// liftable non-equijoin residuals. M0062-0005 added the residual
// path to handle Q21's `l2.l_suppkey <> l1.l_suppkey` correlation
// alongside the equi-pair `l2.l_orderkey = l1.l_orderkey`.
//
// Non-correlated EXISTS already collapses to a single inner-plan
// execution via M0058-0001's IsNonCorrelated cache; converting it
// to a join is unnecessary churn and would lose the cache hit when
// the same EXISTS appears across many queries.
//
// EXISTS subqueries with nested IN / EXISTS that themselves can
// not be unnested are rejected — leaving them as SubPlans is safer
// than introducing a join with a still-correlated inner plan.
func canUnnestExistsExpr(ex *ExistsExpr) bool {
	plan := ex.Plan
	if plan == nil {
		return false
	}
	if ex.IsNonCorrelated {
		return false
	}
	eup := collectExistsUnnestParamsAndResiduals(plan)
	if eup == nil || len(eup.Params) == 0 {
		// Need at least one equijoin to drive the hash join.
		return false
	}
	var hasNestedSub bool
	walkPlanExprs(plan, func(e Expr) {
		if in2, ok := e.(*InExpr); ok && in2.Plan != nil {
			hasNestedSub = true
		}
		if ex2, ok := e.(*ExistsExpr); ok && ex2.Plan != nil {
			hasNestedSub = true
		}
		if sq2, ok := e.(*SubqueryExpr); ok && sq2.Plan != nil {
			hasNestedSub = true
		}
	})
	if hasNestedSub {
		return false
	}
	return true
}

// unnestExistsExpr rewrites a top-level EXISTS / NOT EXISTS
// conjunct into a semi-join / anti-join.
//
//	Filter(EXISTS(SELECT ... WHERE inner = outer.col)     , outer)
//	→  Filter(other_preds, JoinTypeSemi(outer, inner_clone))
//
//	Filter(NOT EXISTS(SELECT ... WHERE inner = outer.col) , outer)
//	→  Filter(other_preds, JoinTypeAnti(outer, inner_clone))
//
// `NOT EXISTS` arrives from the parser as `UnaryOp("NOT",
// ExistsExpr{Negated: false})` — we look for the EXISTS at a
// top-level conjunct position OR wrapped in a single NOT UnaryOp
// at a top-level conjunct position.
//
// The inner plan is cloned with OuterColumnRef → ColumnRef
// replacement so it is self-contained; remaining inner-plan
// optimisation runs via the recursive unnestSubqueriesInPlan call
// below.
//
// The join's output schema is the LEFT (outer) schema only —
// downstream column indices are unchanged from before unnesting.
func unnestExistsExpr(ex *ExistsExpr, outer Node) (Node, error) {
	if !canUnnestExistsExpr(ex) {
		return nil, nil
	}
	eup := collectExistsUnnestParamsAndResiduals(ex.Plan)
	if eup == nil || len(eup.Params) == 0 {
		return nil, nil
	}
	params := eup.Params

	// Find the Filter that contains this ExistsExpr conjunct.
	filter, conjunct := findFilterContainingExistsExpr(outer, ex)
	if filter == nil {
		return nil, nil
	}

	// The EXISTS may sit at a top-level conjunct directly, or be
	// wrapped in a `NOT` UnaryOp (the parser's representation of
	// `NOT EXISTS`). Determine the actual top-level conjunct and
	// whether the EXISTS is negated.
	negated := ex.Negated
	conjuncts := splitAnd(filter.Predicate)
	topConjunct := Expr(nil)
	for _, c := range conjuncts {
		if c == conjunct {
			topConjunct = c
			break
		}
		if u, ok := c.(*UnaryOp); ok && (u.Op == "NOT" || u.Op == "not") {
			if u.Operand == conjunct {
				topConjunct = c
				negated = !negated
				break
			}
		}
	}
	if topConjunct == nil {
		// EXISTS is buried in a non-conjunct context (e.g. an
		// OR branch or inside a function call). Cannot lift to
		// a join without changing semantics.
		return nil, nil
	}

	// Replace OuterColumnRefs with ColumnRefs and clone the inner
	// plan so it no longer carries outer-scope dependencies.
	replace := make(map[*OuterColumnRef]*ColumnRef, len(params))
	for _, p := range params {
		replace[p.OuterRef] = p.SubCol
	}
	innerPlan, err := clonePlanReplacingOuter(ex.Plan, replace)
	if err != nil {
		return nil, err
	}
	// M0062-0005: strip any conjuncts from the cloned inner plan's
	// Filters that still reference an OuterColumnRef. The residual
	// conjuncts have been lifted to the join's Predicate; if they
	// stayed in the inner plan the executor would try to evaluate
	// an unbound OuterColumnRef at build time. The equi-pair
	// conjuncts were already neutralised by the `replace` map (they
	// became self-equality tautologies) — those are also removed
	// for cleanliness.
	if len(eup.Residuals) > 0 {
		stripOuterRefConjuncts(innerPlan)
	}
	innerPlan = unnestSubqueriesInPlan(innerPlan)

	outerChild := filter.Child
	outerWidth := len(outerChild.Output())

	// Build keys. The probe (outer) key uses the outer column ref
	// at its original index; the build (inner) key uses the inner
	// ColumnRef adjusted to point into the inner plan's own
	// schema, NOT a merged schema, because the join's output is
	// outer-only and the build side reads from a row of inner
	// width alone (see executor's openLazyHashJoin path for
	// BuildLeft=false).
	outerKey := &ColumnRef{
		pos:   params[0].OuterRef.Pos(),
		Index: params[0].OuterRef.Index,
		Name:  params[0].OuterRef.Name,
		Type:  params[0].OuterRef.Type,
	}
	// The executor's evalHashKey is given a padded row of width
	// (leftWidth + rightWidth). For semi/anti the right key reads
	// the inner column from the right-side region of that padded
	// row, so its Index must be `outerWidth + innerColIndex`.
	innerKey := &ColumnRef{
		pos:   params[0].SubCol.Pos(),
		Index: outerWidth + params[0].SubCol.Index,
		Name:  params[0].SubCol.Name,
		Type:  params[0].SubCol.Type,
	}

	// Drop the EXISTS conjunct (or its wrapping NOT) entirely;
	// the join encodes the equality predicate via (LeftKey,
	// RightKey) and the negation via Type=Anti.
	newConjuncts := make([]Expr, 0, len(conjuncts))
	for _, c := range conjuncts {
		if c != topConjunct {
			newConjuncts = append(newConjuncts, c)
		}
	}
	if len(newConjuncts) == 0 {
		// All conjuncts consumed — keep the Filter wrapping but
		// with a true-predicate so downstream code that recurses
		// through Filter still finds the join.
		filter.Predicate = &BooleanConst{pos: ex.Pos(), Value: true}
	} else {
		filter.Predicate = combineAnd(newConjuncts)
	}

	joinType := JoinTypeSemi
	if negated {
		joinType = JoinTypeAnti
	}

	// M0062-0005: lift non-equijoin residuals (e.g. Q21's
	// `l2.l_suppkey <> l1.l_suppkey`) into the join's Predicate
	// after rewriting indices: OuterColumnRef → ColumnRef at the
	// outer's original Index, inner ColumnRef → ColumnRef at
	// `outerWidth + originalIndex`. The join evaluates Predicate
	// against the (outer ++ inner) joined row.
	var joinPredicate Expr
	if len(eup.Residuals) > 0 {
		var rewriteIdx func(Expr) Expr
		rewriteIdx = func(e Expr) Expr {
			if e == nil {
				return nil
			}
			switch x := e.(type) {
			case *OuterColumnRef:
				return &ColumnRef{
					pos:   x.Pos(),
					Index: x.Index,
					Name:  x.Name,
					Type:  x.Type,
				}
			case *ColumnRef:
				cl := *x
				cl.Index = outerWidth + x.Index
				return &cl
			case *BinaryOp:
				return &BinaryOp{
					pos:   x.Pos(),
					Op:    x.Op,
					Left:  rewriteIdx(x.Left),
					Right: rewriteIdx(x.Right),
				}
			case *UnaryOp:
				return &UnaryOp{pos: x.Pos(), Op: x.Op, Operand: rewriteIdx(x.Operand)}
			case *FuncCall:
				args := make([]Expr, len(x.Args))
				for i, a := range x.Args {
					args[i] = rewriteIdx(a)
				}
				return &FuncCall{pos: x.Pos(), Name: x.Name, Args: args, Star: x.Star}
			default:
				return e
			}
		}
		residualConj := make([]Expr, 0, len(eup.Residuals))
		for _, r := range eup.Residuals {
			residualConj = append(residualConj, rewriteIdx(r))
		}
		joinPredicate = combineAnd(residualConj)
	}

	join := &Join{
		pos:       ex.Pos(),
		Type:      joinType,
		Algo:      JoinAlgoHash,
		Left:      outerChild,
		Right:     innerPlan,
		Predicate: joinPredicate,
		LeftKey:   outerKey,
		RightKey:  innerKey,
		// Schema is the outer side only — semi/anti joins do not
		// project inner columns.
		schema: append(Schema(nil), outerChild.Output()...),
	}
	filter.Child = join
	return outer, nil
}

// findFilterContainingExistsExpr walks the plan tree looking for
// the Filter whose predicate contains the target ExistsExpr.
func findFilterContainingExistsExpr(node Node, target *ExistsExpr) (*Filter, Expr) {
	if node == nil {
		return nil, nil
	}
	if f, ok := node.(*Filter); ok {
		if c := findExprInExpr(f.Predicate, func(e Expr) bool {
			return e == target
		}); c != nil {
			return f, c
		}
	}
	switch n := node.(type) {
	case *Join:
		if f, c := findFilterContainingExistsExpr(n.Left, target); f != nil {
			return f, c
		}
		return findFilterContainingExistsExpr(n.Right, target)
	case *Filter:
		return findFilterContainingExistsExpr(n.Child, target)
	case *Project:
		return findFilterContainingExistsExpr(n.Child, target)
	case *Aggregate:
		return findFilterContainingExistsExpr(n.Child, target)
	case *Sort:
		return findFilterContainingExistsExpr(n.Child, target)
	case *Limit:
		return findFilterContainingExistsExpr(n.Child, target)
	case *MultiHashJoin:
		for _, tbl := range n.Tables {
			if f, c := findFilterContainingExistsExpr(tbl, target); f != nil {
				return f, c
			}
		}
	}
	return nil, nil
}
