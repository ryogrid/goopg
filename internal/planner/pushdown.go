// Predicate-pushdown for the comma-FROM CROSS-join shape.
//
// `SELECT FROM r, n, s WHERE r.rkey = n.rkey AND s.skey = n.skey`
// initially plans as `Filter(Cross(Cross(r, n), s))` because
// `planFromRangeVars` builds CROSS joins and `planSelect` wraps WHERE
// as a Filter on top. That's correct but Cartesian — for TPC-H scale
// it never finishes. The pushdown pass walks the conjunction in the
// Filter's predicate and rehomes each disjoint-side equality onto
// the deepest Join whose schema contains both sides, promoting that
// Join from CROSS to INNER and (when the equality decomposes
// cleanly) flipping it to JoinAlgoHash with build-side selection
// from `EstimateRows`. Conjuncts that don't span two sides — single-
// table filters, or refs we can't classify — stay on the outer
// Filter.
//
// Scope: only applies to JoinTypeCross nodes. JOIN ... ON predicates
// already live on their Join nodes; LEFT/RIGHT/FULL outer joins
// can't accept extra predicates without changing their semantics.
//
// See docs/design/0003-0001-planner-overview.md.
package planner

// pushPredicatesIntoCrossJoins is the entry point for the pass.
// Returns the (possibly rewritten) tree. The caller passes the node
// it built around the WHERE filter — typically a *Filter wrapping
// the FROM-clause tree, but a raw Join is also accepted for the
// FROM-clause-without-WHERE case where the JOIN ... ON itself has
// the equality.
func pushPredicatesIntoCrossJoins(node Node) Node {
	f, ok := node.(*Filter)
	if !ok {
		return node
	}
	conjuncts := splitAnd(f.Predicate)
	if len(conjuncts) == 0 {
		return node
	}
	var remaining []Expr
	for _, c := range conjuncts {
		if !pushOneConjunct(f.Child, c) {
			remaining = append(remaining, c)
		}
	}
	if len(remaining) == 0 {
		return f.Child
	}
	f.Predicate = combineAnd(remaining)
	return f
}

// pushOneConjunct attempts to push a single conjunct down the join
// tree. Returns true when the conjunct landed on a Join — the
// caller drops it from the residual filter list.
func pushOneConjunct(node Node, c Expr) bool {
	j, ok := node.(*Join)
	if !ok {
		return false
	}
	// Try children first so the deepest matching Join wins.
	if pushOneConjunct(j.Left, c) {
		return true
	}
	if pushOneConjunct(j.Right, c) {
		return true
	}
	if j.Type != JoinTypeCross {
		return false
	}
	leftWidth := len(j.Left.Output())
	totalWidth := leftWidth + len(j.Right.Output())
	side := classifyConjunctSide(c, leftWidth, totalWidth)
	if side != sideMixed {
		return false
	}
	// Predicate spans both sides — promote the Join.
	j.Type = JoinTypeInner
	j.Predicate = c
	if lk, rk, okSplit := splitEqualityForHash(c, leftWidth); okSplit {
		j.LeftKey = lk
		j.RightKey = rk
		j.Algo = JoinAlgoHash
		lRows := EstimateRows(j.Left)
		rRows := EstimateRows(j.Right)
		// Cost-driven INNER algorithm pick when stats are
		// present; rule-based hash is the fallback (see
		// docs/design/0006-0004-join-algorithm-selection.md).
		if algo, ok := chooseInnerJoinAlgo(lRows, rRows); ok {
			j.Algo = algo
		}
		if j.Algo == JoinAlgoHash && lRows > 0 && rRows > 0 && lRows < rRows {
			j.BuildLeft = true
		}
	}
	return true
}

// sideOutOfScope is a sentinel returned by classifyConjunctSide
// when the conjunct references a column outside the current Join's
// schema (or carries an OuterColumnRef / subquery). Encoded as -1
// so it doesn't collide with the regular joinSide values.
const sideOutOfScope joinSide = -1

// classifyConjunctSide reports whether c references columns only on
// the left, only on the right, both, or includes refs outside the
// current Join's schema. OuterColumnRef (correlated subquery) is
// out-of-scope for pushdown — those resolve against an enclosing
// scope, not this Join's schema.
func classifyConjunctSide(c Expr, leftWidth, totalWidth int) joinSide {
	state := sideUnknown
	out := false
	walkColumnRefs(c, func(idx int) {
		if out {
			return
		}
		if idx < 0 || idx >= totalWidth {
			out = true
			return
		}
		var s joinSide
		if idx < leftWidth {
			s = sideLeft
		} else {
			s = sideRight
		}
		switch state {
		case sideUnknown:
			state = s
		case sideLeft, sideRight:
			if state != s {
				state = sideMixed
			}
		}
	}, func() { out = true })
	if out {
		return sideOutOfScope
	}
	return state
}

// walkColumnRefs invokes onIdx for every ColumnRef.Index in e and
// onOuter for any encounter that should disqualify pushdown
// (OuterColumnRef, SubqueryExpr, ExistsExpr — anything whose
// runtime value depends on state outside this Join's schema).
func walkColumnRefs(e Expr, onIdx func(int), onOuter func()) {
	switch x := e.(type) {
	case nil:
	case *ColumnRef:
		onIdx(x.Index)
	case *OuterColumnRef:
		onOuter()
	case *SubqueryExpr, *ExistsExpr, *InExpr:
		// Subqueries can reference outer columns; treat as out
		// of scope rather than walking into the inner plan.
		onOuter()
	case *BinaryOp:
		walkColumnRefs(x.Left, onIdx, onOuter)
		walkColumnRefs(x.Right, onIdx, onOuter)
	case *UnaryOp:
		walkColumnRefs(x.Operand, onIdx, onOuter)
	case *FuncCall:
		for _, a := range x.Args {
			walkColumnRefs(a, onIdx, onOuter)
		}
	case *CaseExpr:
		walkColumnRefs(x.Operand, onIdx, onOuter)
		for _, w := range x.Whens {
			walkColumnRefs(w.When, onIdx, onOuter)
			walkColumnRefs(w.Then, onIdx, onOuter)
		}
		walkColumnRefs(x.Else, onIdx, onOuter)
	case *ExtractExpr:
		walkColumnRefs(x.Source, onIdx, onOuter)
	}
}

// splitAnd flattens a left-associated AND tree into its leaves.
// `(a AND b) AND c` → [a, b, c]. Single-leaf inputs become a
// 1-element slice; nil input returns nil.
func splitAnd(e Expr) []Expr {
	if e == nil {
		return nil
	}
	bin, ok := e.(*BinaryOp)
	if !ok || bin.Op != "AND" {
		return []Expr{e}
	}
	out := splitAnd(bin.Left)
	out = append(out, splitAnd(bin.Right)...)
	return out
}

// combineAnd is splitAnd's inverse: builds a left-associated
// AND tree from the given conjuncts. nil/empty input returns nil.
func combineAnd(conjuncts []Expr) Expr {
	if len(conjuncts) == 0 {
		return nil
	}
	out := conjuncts[0]
	for _, c := range conjuncts[1:] {
		out = &BinaryOp{pos: out.Pos(), Op: "AND", Left: out, Right: c}
	}
	return out
}
