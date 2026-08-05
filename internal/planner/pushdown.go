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
	case *SetOp, *RecursiveUnion:
		// M0125-0034 (C1). A FROM-clause subquery whose body is a set
		// operation is a legal join input, and its Output() is the
		// narrow projected schema the outer query resolved its
		// ColumnRefs against — exactly what this name check needs.
		// Without a case here the walk returned only the OTHER side's
		// names, allColumnRefNamesInScope answered false for every
		// conjunct spanning the set operation, and pushOneConjunct
		// declined before it ever reached its own containsSetOp guard.
		// The visible symptom was a `Nested Loop (CROSS)` with the
		// equi-join demoted to a Filter — TPC-DS Q8 Q14 Q30 Q54 Q64
		// Q65 Q71 Q81 (analysis/m0125-0026-timeout-plans/README.md,
		// "The dominant mechanism"). No recursion into the branches:
		// a branch's internal column names are NOT in the outer
		// query's scope, only the set operation's own output is.
		for _, c := range n.Output() {
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
		//
		// M0125-0002 commit 7: `total` is a SECOND and unconditional
		// escape, and the two must not be confused. The leftNames
		// escape covers "we cannot enumerate the NODE's columns" and
		// deliberately falls back on the index verdict; `total` covers
		// "we cannot enumerate the CONJUNCT", where the index verdict
		// is no fallback at all — classifyConjunctSide is built on
		// walkColumnRefsImpl, which has no `default:` either, so an
		// unenumerated kind is invisible to BOTH tests and a conjunct
		// wrapping e.g. an *ArraySubqueryExpr reads as conclusively
		// sideLeft on its other operand alone.
		allIn := true
		total := visitColumnRefsByName(c, func(name string) {
			if !leftNames[name] {
				allIn = false
			}
		})
		if !total {
			remaining = append(remaining, c)
			continue
		}
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
	total := visitColumnRefsByName(c, func(name string) {
		if !names[name] {
			allIn = false
		}
	})
	return total && allIn
}

// pushOneConjunct attempts to push a single conjunct down the join
// tree. Returns true when the conjunct landed on a Join — the
// caller drops it from the residual filter list.
func pushOneConjunct(node Node, c Expr) bool {
	// M0127-P5.9-b (08 §3): a searched subtree is opaque to this pass. Its
	// INTERNAL joins address their own per-joinrel layouts — only its root is
	// republished in the statement's binding order (03 §10) — while `c` is
	// written in FROM-cumulative coordinates, so rehoming it onto one of them
	// would evaluate it against the wrong columns. The search has already
	// placed every clause it admitted; what reaches here is what it left.
	if isSearchedTree(node) {
		return false
	}
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
		// M0125-0034 retires M0097-0058's blanket bailout here (and
		// the hash-algorithm one below). That guard declined the
		// CROSS→INNER promotion whenever either side contained a
		// SetOp/RecursiveUnion, on the premise that the predicate's
		// ColumnRef indices "do not match the subquery's narrow output
		// schema" and would drive an index-out-of-bounds panic in the
		// build-side drain (`index out of range [57] with length 1`,
		// TPC-DS Q8, commit 9ddbc679).
		//
		// The premise does not survive inspection: `SetOp.Output()` IS
		// the narrow projected schema, so `leftWidth`/`totalWidth` here
		// and `len(o.left.Schema())` in joinOp agree, and the executor
		// evaluates both join keys against a full-width padded keyRow
		// (operators_join_agg.go buildHashRight — `keyRow` is
		// leftWidth+rightWidth wide with the build row copied into its
		// tail), never against the bare 1-wide build row. What actually
		// produced Q8's CROSS in the shape the guard was written for
		// was the missing *SetOp case in collectScanOutputNames above,
		// which is now present; where the guard did fire on its own
		// (a Project-wrapped set operation) lifting it yields a hash
		// join with byte-identical answers.
		//
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
			// The M0097-0058 hash-algorithm bailout for a
			// set-operation side is retired here too; see the
			// note on the promotion above.
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

// visitColumnRefNodes invokes onRef for every *ColumnRef NODE in e
// (index and name together) and onOuter for any encounter that should
// disqualify pushdown — same traversal contract as walkColumnRefs
// below, which it delegates to for coverage parity.
func visitColumnRefNodes(e Expr, onRef func(*ColumnRef), onOuter func()) {
	walkColumnRefsImpl(e, nil, onRef, onOuter)
}

// walkColumnRefs invokes onIdx for every ColumnRef.Index in e and
// onOuter for any encounter that should disqualify pushdown
// (OuterColumnRef, SubqueryExpr, ExistsExpr — anything whose
// runtime value depends on state outside this Join's schema).
func walkColumnRefs(e Expr, onIdx func(int), onOuter func()) {
	walkColumnRefsImpl(e, onIdx, nil, onOuter)
}

func walkColumnRefsImpl(e Expr, onIdx func(int), onRef func(*ColumnRef), onOuter func()) {
	switch x := e.(type) {
	case nil:
	case *ColumnRef:
		if onIdx != nil {
			onIdx(x.Index)
		}
		if onRef != nil {
			onRef(x)
		}
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
		walkColumnRefsImpl(x.Operand, onIdx, onRef, onOuter)
		for _, item := range x.List {
			walkColumnRefsImpl(item, onIdx, onRef, onOuter)
		}
	case *BinaryOp:
		walkColumnRefsImpl(x.Left, onIdx, onRef, onOuter)
		walkColumnRefsImpl(x.Right, onIdx, onRef, onOuter)
	case *UnaryOp:
		walkColumnRefsImpl(x.Operand, onIdx, onRef, onOuter)
	case *FuncCall:
		for _, a := range x.Args {
			walkColumnRefsImpl(a, onIdx, onRef, onOuter)
		}
	case *CaseExpr:
		walkColumnRefsImpl(x.Operand, onIdx, onRef, onOuter)
		for _, w := range x.Whens {
			walkColumnRefsImpl(w.When, onIdx, onRef, onOuter)
			walkColumnRefsImpl(w.Then, onIdx, onRef, onOuter)
		}
		walkColumnRefsImpl(x.Else, onIdx, onRef, onOuter)
	case *ExtractExpr:
		walkColumnRefsImpl(x.Source, onIdx, onRef, onOuter)
	case *CastExpr:
		// A cast can wrap an outer-side ColumnRef (e.g. `cast(outer.col AS
		// text) = inner.col`). Missing this case made classifyConjunctSide
		// blind to the wrapped ref, so a mixed conjunct could be misread as
		// single-side and wrongly pushed below a LEFT JOIN. Keep in lockstep
		// with shiftColumnRefsBy's CastExpr case.
		walkColumnRefsImpl(x.Operand, onIdx, onRef, onOuter)
	case *IsNullExpr:
		walkColumnRefsImpl(x.Operand, onIdx, onRef, onOuter)
	case *IsBoolExpr:
		walkColumnRefsImpl(x.Operand, onIdx, onRef, onOuter)
	case *IsDistinctFromExpr:
		walkColumnRefsImpl(x.Left, onIdx, onRef, onOuter)
		walkColumnRefsImpl(x.Right, onIdx, onRef, onOuter)
	case *CollateExpr:
		walkColumnRefsImpl(x.Operand, onIdx, onRef, onOuter)
	case *RowExpr:
		for _, el := range x.Elems {
			walkColumnRefsImpl(el, onIdx, onRef, onOuter)
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

// SplitAnd is the exported wrapper for the predicate splitter.
// M0126-0006: the runtime fusion predicate in the executor needs it.
func SplitAnd(e Expr) []Expr { return splitAnd(e) }

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
