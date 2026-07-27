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

import "github.com/goopg/goopg/internal/parser"

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

// collectScanOutputNames adds the output column names of every
// SeqScan/IndexScan/Project/Aggregate in n's subtree into names. It
// is the shared name-gathering walk behind allColumnRefNamesInScope
// and the lateral outer-qual scope check.
func collectScanOutputNames(n Node, names map[string]bool) {
	if n == nil {
		return
	}
	switch x := n.(type) {
	case *SeqScan:
		for _, col := range x.Output() {
			names[col.Name] = true
		}
	case *IndexScan:
		for _, col := range x.Output() {
			names[col.Name] = true
		}
	case *MultiHashJoin:
		for _, t := range x.Tables {
			collectScanOutputNames(t, names)
		}
	case *Join:
		collectScanOutputNames(x.Left, names)
		collectScanOutputNames(x.Right, names)
	case *Filter:
		collectScanOutputNames(x.Child, names)
	case *Project:
		for _, c := range x.Output() {
			names[c.Name] = true
		}
	case *Sort:
		collectScanOutputNames(x.Child, names)
	case *Aggregate:
		for _, c := range x.Output() {
			names[c.Name] = true
		}
	case *Values:
		// Virtual catalog relations (pg_class, pg_namespace, …) plan
		// as a Values node with a VirtualSource; expose its columns so
		// a qual over a catalog table is recognised in scope.
		for _, c := range x.Output() {
			names[c.Name] = true
		}
	case *CTEScan:
		for _, c := range x.Output() {
			names[c.Name] = true
		}
		// Recurse into the inlined CTE body so columns from the
		// underlying plan (scan/project/etc.) are also visible.
		collectScanOutputNames(x.Child, names)
	case *MaterializedCTEScan:
		for _, c := range x.Output() {
			names[c.Name] = true
		}
	}
}

// pushOuterQualsIntoLaterals pushes a residual WHERE conjunct that
// references only the outer (left) side of a LATERAL join down onto
// that join's outer child. A lateral nested-loop re-opens the right
// side once per surviving outer row, so without this an outer-only
// restriction invokes the lateral RHS for EVERY outer row. That is
// merely wasteful for an ordinary data-returning RHS, but a
// side-effecting RHS — pg_amcheck's `FROM pg_catalog.pg_class c,
// verify_heapam(relation := c.oid) v WHERE c.oid = N` — raises an
// error on the first non-heap relation (verify_heapam rejects an
// index/sequence OID) instead of being skipped, aborting the whole
// scan. PostgreSQL applies such a single-relation qual to the outer
// scan before the nested loop; pushing it onto j.Left matches that
// placement and makes pg_amcheck's relation-scoped probes resolve.
//
// Conservative by design: only the lateral Join that is the residual
// Filter's direct child is considered (the FROM-root lateral
// comma-join pg_amcheck emits), and each pushed conjunct must
// reference columns only within the outer child's schema, checked by
// both index range (classifyConjunctSide == sideLeft) and column
// name (collectScanOutputNames). The outer child occupies the join's
// leading columns, so a left-only conjunct's indices already align
// with j.Left.Output() — no remapping needed.
func pushOuterQualsIntoLaterals(node Node) Node {
	f, ok := node.(*Filter)
	if !ok {
		return node
	}
	j, ok := f.Child.(*Join)
	if !ok || !j.Lateral {
		return node
	}
	leftWidth := len(j.Left.Output())
	totalWidth := leftWidth + len(j.Right.Output())
	leftNames := map[string]bool{}
	collectScanOutputNames(j.Left, leftNames)

	var pushed, remaining []Expr
	for _, c := range splitAnd(f.Predicate) {
		if classifyConjunctSide(c, leftWidth, totalWidth) != sideLeft {
			remaining = append(remaining, c)
			continue
		}
		// Name-in-scope guard against width-based mis-classification.
		// j.Left is the join's direct left child, so a sideLeft verdict
		// (all ColumnRef indices < leftWidth) already pins the refs to
		// the left output; the name check is only an extra safety, so a
		// node whose columns we can't enumerate (empty leftNames) still
		// pushes on the conclusive index classification.
		allIn := true
		visitColumnRefsByName(c, func(name string) {
			if !leftNames[name] {
				allIn = false
			}
		})
		if !allIn && len(leftNames) > 0 {
			remaining = append(remaining, c)
			continue
		}
		pushed = append(pushed, c)
	}
	if len(pushed) == 0 {
		return node
	}
	j.Left = &Filter{pos: pushed[0].Pos(), Child: j.Left, Predicate: combineAnd(pushed)}
	if len(remaining) == 0 {
		return j
	}
	f.Predicate = combineAnd(remaining)
	return f
}

// allColumnRefNamesInScope reports whether every named ColumnRef in
// c references a column that appears in the output schema of at
// least one SeqScan/IndexScan in j's subtree. Used to guard
// pushOneConjunct against width-based mis-classification when a
// conjunct's ColumnRef indices coincidentally fall in this Join's
// width range but actually point at tables outside the subtree.
func allColumnRefNamesInScope(c Expr, j *Join) bool {
	names := map[string]bool{}
	collectScanOutputNames(j.Left, names)
	collectScanOutputNames(j.Right, names)
	allIn := true
	visitColumnRefsByName(c, func(name string) {
		if !names[name] {
			allIn = false
		}
	})
	return allIn
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
	leftWidth := len(j.Left.Output())
	totalWidth := leftWidth + len(j.Right.Output())
	side := classifyConjunctSide(c, leftWidth, totalWidth)
	if side != sideMixed {
		return false
	}
	// Width-based classification can produce a sideMixed verdict for
	// a conjunct whose ColumnRefs (in their original FROM-order
	// indices) happen to fall in this Join's [0, totalWidth) range
	// but actually reference tables outside this Join's subtree
	// (e.g. TPC-H Q9's `ps_partkey = l_partkey` mis-classified onto
	// a 4-table Inner Join that doesn't include partsupp because
	// partsupp's global FROM offset coincidentally lies in the
	// 4-table subset's width range). Validate by NAME against the
	// scans in this Join's subtree before committing to push.
	if !allColumnRefNamesInScope(c, j) {
		return false
	}
	if j.Type == JoinTypeCross {
		// M0097-0058: when either side contains a set operation
		// (SetOp/RecursiveUnion), skip promotion to Inner Join.
		// The column indices in the predicate were resolved against
		// the global FROM-clause schema and do not match the
		// subquery's narrow output schema, causing index-out-of-
		// bounds panics when the executor drains the build side
		// (e.g. TPC-DS Q8: INTERSECT in FROM subquery).
		if containsSetOp(j.Left) || containsSetOp(j.Right) {
			return false // predicate stays in the outer Filter
		}
		// Predicate spans both sides — promote the Join.
		j.Type = JoinTypeInner
		j.Predicate = c
		// First try a direct equality decomposition.
		lk, rk, okSplit := splitEqualityForHash(c, leftWidth)
		if !okSplit {
			// M0058-0004: if the predicate is an OR-of-ANDs where every
			// branch shares the same equijoin (Q19 shape), use that
			// equijoin as the hash key. The full OR remains as the join
			// predicate so non-key conjuncts in each branch still apply.
			if eq := pickCommonOrEquijoin(c, leftWidth); eq != nil {
				lk, rk, okSplit = splitEqualityForHash(eq, leftWidth)
			}
		}
		if okSplit {
			j.LeftKey = lk
			j.RightKey = rk
			// M0097-0058: skip hash-join when a set-operation
			// subquery is on either side (column-index mismatch
			// crash, e.g. TPC-DS Q8 INTERSECT in FROM subquery).
			if !containsSetOp(j.Left) && !containsSetOp(j.Right) {
				j.Algo = JoinAlgoHash
				lRows := EstimateRows(j.Left)
				rRows := EstimateRows(j.Right)
				if algo, ok := chooseInnerJoinAlgo(lRows, rRows); ok {
					j.Algo = algo
				}
				if j.Algo == JoinAlgoHash {
					if lRows > 0 && rRows > 0 && lRows < rRows {
						j.BuildLeft = true
					}
					// M0054-0010: small-dimension override.
					leftSmall := IsSmallDimensionSide(j.Left)
					rightSmall := IsSmallDimensionSide(j.Right)
					if leftSmall && !rightSmall {
						j.BuildLeft = true
					} else if rightSmall && !leftSmall {
						j.BuildLeft = false
					}
				}
			}
		}
		return true
	}
	// Join is already Inner/Hash — append the conjunct to the
	// existing predicate via AND.  This handles the case where
	// the bushy DP or a prior pushdown pass consumed one edge
	// but left a different spanning conjunct unapplied.
	if j.Type == JoinTypeInner {
		if j.Predicate == nil {
			j.Predicate = c
		} else {
			j.Predicate = &BinaryOp{pos: c.Pos(), Op: parser.OpAnd, Left: j.Predicate, Right: c}
		}
		return true
	}
	return false
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
	case *SubqueryExpr, *ExistsExpr, *MultiAssignSubqElem, *MultiAssignSubqRow:
		// Subqueries can reference outer columns; treat as out
		// of scope rather than walking into the inner plan.
		onOuter()
	case *InExpr:
		// `col IN (subquery)` is out-of-scope (the subquery may
		// reference outer columns). `col IN (literal, ...)` is
		// fine — walk the operand and the literal list. (M0061
		// fix: previously treated all InExpr as out-of-scope,
		// which blocked Q19 / Q22 from pushdown because of their
		// `p_container IN (...)` / `c_phone IN (...)` literal
		// lists.)
		if x.Plan != nil {
			onOuter()
			return
		}
		walkColumnRefs(x.Operand, onIdx, onOuter)
		for _, item := range x.List {
			walkColumnRefs(item, onIdx, onOuter)
		}
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
	case *CastExpr:
		// A cast can wrap an outer-side ColumnRef (e.g. `cast(outer.col AS
		// text) = inner.col`). Missing this case made classifyConjunctSide
		// blind to the wrapped ref, so a mixed conjunct could be misread as
		// single-side and wrongly pushed below a LEFT JOIN. Keep in lockstep
		// with shiftColumnRefsBy's CastExpr case.
		walkColumnRefs(x.Operand, onIdx, onOuter)
	case *IsNullExpr:
		walkColumnRefs(x.Operand, onIdx, onOuter)
	case *IsBoolExpr:
		walkColumnRefs(x.Operand, onIdx, onOuter)
	case *IsDistinctFromExpr:
		walkColumnRefs(x.Left, onIdx, onOuter)
		walkColumnRefs(x.Right, onIdx, onOuter)
	case *CollateExpr:
		walkColumnRefs(x.Operand, onIdx, onOuter)
	case *RowExpr:
		for _, el := range x.Elems {
			walkColumnRefs(el, onIdx, onOuter)
		}
	}
}

// pickCommonOrEquijoin scans the OR-of-ANDs predicate `c` and
// returns one equijoin equality `t1.col = t2.col` that appears in
// every OR branch and that splits cleanly across the [0,leftWidth)
// vs [leftWidth,...) ColumnRef-index boundary. Returns nil when no
// such equijoin exists. (M0058-0004.)
func pickCommonOrEquijoin(c Expr, leftWidth int) *BinaryOp {
	commons := plannerCommonEquijoinsAcrossOr(c)
	for _, eq := range commons {
		if _, _, ok := splitEqualityForHash(eq, leftWidth); ok {
			return eq
		}
	}
	return nil
}

// splitAnd flattens a left-associated AND tree into its leaves.
// `(a AND b) AND c` → [a, b, c]. Single-leaf inputs become a
// 1-element slice; nil input returns nil.
func splitAnd(e Expr) []Expr {
	if e == nil {
		return nil
	}
	bin, ok := e.(*BinaryOp)
	if !ok || bin.Op != parser.OpAnd {
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
		out = &BinaryOp{pos: out.Pos(), Op: parser.OpAnd, Left: out, Right: c}
	}
	return out
}
