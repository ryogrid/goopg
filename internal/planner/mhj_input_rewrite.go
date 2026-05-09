package planner

import "github.com/goopg/goopg/internal/parser"

// Single-table predicate routing into scan inputs (M0054-0006a-pre).
//
// The M0054-0002 EXPLAIN baseline showed several queries doing
// `Seq Scan` on tables that DO have a usable B-tree index when the
// constant-RHS predicate sat in a `*Filter` wrapper above an
// outer-binary HashJoin or in a `*MultiHashJoin.Filters` slice. The
// existing index-selection code (`planIndexScanFromWhere` /
// `tryRangeIndexScan`) only fires for single-table SELECTs at
// `planSelect` time — once the bushy join tree is built, no pass
// re-runs index selection on the inputs.
//
// This file adds a generic post-pass that walks the plan tree once
// after `rewriteMultiWayChain`, and for every `*Filter` it finds (or
// `*MultiHashJoin.Filters` directly), groups single-table
// constant-RHS conjuncts by (target SeqScan, column) and replaces
// the SeqScan with an IndexScan (equality `Key` or range
// `LowKey`/`HighKey`). The pass is general; it doesn't know about
// TPC-H. Whenever a sub-tree contains a SeqScan AND a constant-RHS
// predicate against an indexable column, that scan is now promoted.
//
// Conventions (mirror upstream + the existing plan-time index-scan
// builder):
//
//   - Equality (`Op == "="`) — IndexScan.Key set; conjunct DROPPED
//     from the surrounding Filter (the IndexScan probe is exact for
//     single-column indexes; for composite-leading-column probes
//     M0053-0001 already enforces correctness).
//   - `>=` / `<=` — inclusive bounds. The conjunct is KEPT in the
//     Filter (mirrors `tryRangeIndexScan`'s `Predicate: fullPred`).
//   - `>` / `<` — strict bounds, but RangeScan is inclusive only;
//     the conjunct is also KEPT so the boundary false-positive is
//     filtered out.
//
//   - Constants of the same kinds the existing path accepts
//     (`*IntegerConst`, `*NumericConst`, `*StringConst`,
//     `*TypedStringLit`, `*ParamRef`).
//   - Aggregate / WindowAgg subtrees are NOT crossed.
//   - Column-name ambiguity (same name on more than one SeqScan in
//     the subtree) declines the rewrite to keep the pass safe under
//     self-joins.

import (
	"github.com/goopg/goopg/internal/catalog"
)

// rewriteScanInputsWithSingleTablePredicates is the entry point.
// Mutates the plan tree in place and returns the (possibly
// substituted) root.
func rewriteScanInputsWithSingleTablePredicates(n Node, cat catalog.Catalog) Node {
	if n == nil || cat == nil {
		return n
	}
	switch x := n.(type) {
	case *Filter:
		x.Child = rewriteScanInputsWithSingleTablePredicates(x.Child, cat)
		x.Predicate = absorbConjunctsIntoSubtree(x.Predicate, x, cat)
		if x.Predicate == nil {
			return x.Child
		}
		return x
	case *Project:
		x.Child = rewriteScanInputsWithSingleTablePredicates(x.Child, cat)
		return x
	case *Sort:
		x.Child = rewriteScanInputsWithSingleTablePredicates(x.Child, cat)
		return x
	case *Limit:
		x.Child = rewriteScanInputsWithSingleTablePredicates(x.Child, cat)
		return x
	case *Join:
		x.Left = rewriteScanInputsWithSingleTablePredicates(x.Left, cat)
		x.Right = rewriteScanInputsWithSingleTablePredicates(x.Right, cat)
		return x
	case *MultiHashJoin:
		for i := range x.Tables {
			x.Tables[i] = rewriteScanInputsWithSingleTablePredicates(x.Tables[i], cat)
		}
		rewriteMHJInputsWithSingleTablePredicates(x, cat)
		// M0071-0004: push remaining single-source non-indexable
		// filters down onto matching Tables[i] so build-side
		// cardinality drops before MHJ hash-builds.
		pushSingleSourceFiltersIntoMHJTables(x)
		return x
	case *Aggregate:
		x.Child = rewriteScanInputsWithSingleTablePredicates(x.Child, cat)
		return x
	case *WindowAgg:
		x.Child = rewriteScanInputsWithSingleTablePredicates(x.Child, cat)
		return x
	}
	return n
}

// scanParentRef remembers where a SeqScan lives in the plan tree
// so we can swap it for an IndexScan when a predicate is absorbed.
// Only one of the (parentObj, mhParent+mhIndex) shapes is set —
// the mhParent variant exists because `MultiHashJoin.Tables` is a
// slice slot, not a named field, and needs a numeric index.
type scanParentRef struct {
	topParent *Filter
	parentObj any
	field     string
	mhParent  *MultiHashJoin
	mhIndex   int
}

// scanColumnKey identifies a (SeqScan, column-name) pair used as
// the grouping key for predicate absorption.
type scanColumnKey struct {
	scan *SeqScan
	col  string
}

// scanBounds is the per-(scan, column) accumulator: equality key (if
// any) plus inclusive low/high bounds (if any). The equality wins
// over range when both are present (`Key` excludes `LowKey`/`HighKey`
// per `IndexScan` semantics in `internal/planner/plan.go`).
type scanBounds struct {
	eqKey      Expr // when set: IndexScan.Key
	loKey      Expr // when set: IndexScan.LowKey
	hiKey      Expr // when set: IndexScan.HighKey
	eqConjunct Expr // the original conjunct that supplied eqKey
	parent     scanParentRef
}

// absorbConjunctsIntoSubtree walks the AND-tree of `pred`, groups
// matching conjuncts by (scan, column), rewrites each group's
// SeqScan into an IndexScan when an index exists, and returns the
// predicate with absorbed conjuncts removed (nil when none remain).
func absorbConjunctsIntoSubtree(pred Expr, parent *Filter, cat catalog.Catalog) Expr {
	if pred == nil {
		return nil
	}
	conjs := splitPlannerAnd(pred)

	// First pass: classify each conjunct.
	groups := map[scanColumnKey]*scanBounds{}
	type classified struct {
		conj    Expr
		key     scanColumnKey
		op      parser.OpCode
		val     Expr
		matched bool
	}
	classifs := make([]classified, 0, len(conjs))
	for _, c := range conjs {
		col, op, val, ok := matchSingleTableConstantPredicate(c)
		if !ok {
			classifs = append(classifs, classified{conj: c})
			continue
		}
		target, ref, ok := findUniqueSeqScanByColumn(parent.Child, col.Name, parent)
		if !ok || target == nil {
			classifs = append(classifs, classified{conj: c})
			continue
		}
		k := scanColumnKey{scan: target, col: col.Name}
		b, exists := groups[k]
		if !exists {
			b = &scanBounds{parent: ref}
			groups[k] = b
		}
		switch op {
		case parser.OpEq:
			if b.eqKey == nil {
				b.eqKey = val
				b.eqConjunct = c
			}
		case parser.OpGe, parser.OpGt:
			if b.loKey == nil {
				b.loKey = val
			}
		case parser.OpLe, parser.OpLt:
			if b.hiKey == nil {
				b.hiKey = val
			}
		}
		classifs = append(classifs, classified{
			conj: c, key: k, op: op, val: val, matched: true,
		})
	}

	// Second pass: pick at most one group per SeqScan to rewrite
	// (an IndexScan can only point at one index). Equality groups
	// win over range groups; the first encountered wins ties.
	type scanChoice struct {
		key       scanColumnKey
		bounds    *scanBounds
		isEquality bool
	}
	chosen := map[*SeqScan]scanChoice{}
	for k, b := range groups {
		idx := findBTreeIndexForColumn(cat, k.scan.Table, k.col)
		if idx == nil {
			continue
		}
		// Skip if no actionable bound was collected.
		if b.eqKey == nil && b.loKey == nil && b.hiKey == nil {
			continue
		}
		isEq := b.eqKey != nil
		prev, ok := chosen[k.scan]
		if !ok || (isEq && !prev.isEquality) {
			chosen[k.scan] = scanChoice{key: k, bounds: b, isEquality: isEq}
		}
	}

	// Apply chosen rewrites + remember which conjuncts to drop.
	dropEqConjuncts := make(map[Expr]struct{})
	for ss, ch := range chosen {
		idx := findBTreeIndexForColumn(cat, ss.Table, ch.key.col)
		if idx == nil {
			continue
		}
		var newScan *IndexScan
		if ch.isEquality {
			newScan = &IndexScan{
				pos: ss.Pos(), Table: ss.Table, Alias: ss.Alias, Index: idx,
				Key: ch.bounds.eqKey, schema: ss.Output(),
			}
		} else {
			newScan = &IndexScan{
				pos: ss.Pos(), Table: ss.Table, Alias: ss.Alias, Index: idx,
				LowKey: ch.bounds.loKey, HighKey: ch.bounds.hiKey,
				schema: ss.Output(),
			}
		}
		if !replaceNodeAtParentSlot(ch.bounds.parent, ss, newScan) {
			continue
		}
		// Equality conjuncts get dropped (single-column IndexScan
		// exact-equality probe). Range conjuncts stay so the
		// surrounding Filter handles strict-bound boundary cases.
		if ch.isEquality && ch.bounds.eqConjunct != nil {
			dropEqConjuncts[ch.bounds.eqConjunct] = struct{}{}
		}
	}

	// Rebuild the predicate, dropping absorbed equality conjuncts.
	kept := conjs[:0]
	for _, c := range conjs {
		if _, drop := dropEqConjuncts[c]; drop {
			continue
		}
		kept = append(kept, c)
	}
	if len(kept) == 0 {
		return nil
	}
	return joinPlannerAnd(kept)
}

// matchSingleTableConstantPredicate recognises one of:
//
//   col = <const>      // op = "="
//   col >= <const>     // op = ">="
//   col >  <const>     // op = ">"
//   col <= <const>     // op = "<="
//   col <  <const>     // op = "<"
//
// (or any of those with the const on the left; the operator is
// commuted to keep the column on the left.)
//
// Returns (col, canonical-op, key-const, ok).
func matchSingleTableConstantPredicate(f Expr) (*ColumnRef, parser.OpCode, Expr, bool) {
	bop, ok := f.(*BinaryOp)
	if !ok {
		return nil, parser.OpUnknown, nil, false
	}
	switch bop.Op {
	case parser.OpEq, parser.OpLt, parser.OpLe, parser.OpGt, parser.OpGe:
	default:
		return nil, parser.OpUnknown, nil, false
	}

	var col *ColumnRef
	var key Expr
	colOnLeft := true
	if c, lok := bop.Left.(*ColumnRef); lok {
		col = c
		key = bop.Right
	} else if c, rok := bop.Right.(*ColumnRef); rok {
		col = c
		key = bop.Left
		colOnLeft = false
	} else {
		return nil, parser.OpUnknown, nil, false
	}
	if _, isCol := key.(*ColumnRef); isCol {
		return nil, parser.OpUnknown, nil, false
	}
	switch key.(type) {
	case *IntegerConst, *NumericConst, *StringConst, *TypedStringLit, *ParamRef:
	default:
		return nil, parser.OpUnknown, nil, false
	}
	canonOp := bop.Op
	if !colOnLeft {
		canonOp = flipRangeOpForRewrite(canonOp)
	}
	return col, canonOp, key, true
}

// flipRangeOpForRewrite flips the comparison so the column is
// canonically on the left. Keeps `=` as is.
func flipRangeOpForRewrite(op parser.OpCode) parser.OpCode {
	switch op {
	case parser.OpLt:
		return parser.OpGt
	case parser.OpLe:
		return parser.OpGe
	case parser.OpGt:
		return parser.OpLt
	case parser.OpGe:
		return parser.OpLe
	}
	return op
}

// findUniqueSeqScanByColumn walks `n` and returns the unique
// SeqScan whose Output() contains `colName`, plus a `scanParentRef`
// that pins its parent slot for in-place replacement. Returns
// (nil, _, false) when no match or an ambiguous match is found.
//
// Aggregate / WindowAgg subtrees are NOT crossed (query-phase
// boundary). MultiHashJoin descent IS allowed: an outer Filter
// wrapping an MHJ is the case M0054-0006a-pre is meant to handle
// (the SeqScan lives in `mh.Tables[i]`).
func findUniqueSeqScanByColumn(n Node, colName string, topParent *Filter) (*SeqScan, scanParentRef, bool) {
	var hit *SeqScan
	var hitRef scanParentRef
	collide := false

	var walk func(node Node, ref scanParentRef)
	walk = func(node Node, ref scanParentRef) {
		if node == nil || collide {
			return
		}
		switch x := node.(type) {
		case *SeqScan:
			for _, c := range x.Output() {
				if c.Name == colName {
					if hit != nil {
						collide = true
						return
					}
					hit = x
					hitRef = ref
					return
				}
			}
		case *Filter:
			walk(x.Child, scanParentRef{topParent: topParent, parentObj: x, field: "Child"})
		case *Project:
			walk(x.Child, scanParentRef{topParent: topParent, parentObj: x, field: "Child"})
		case *Sort:
			walk(x.Child, scanParentRef{topParent: topParent, parentObj: x, field: "Child"})
		case *Limit:
			walk(x.Child, scanParentRef{topParent: topParent, parentObj: x, field: "Child"})
		case *Join:
			walk(x.Left, scanParentRef{topParent: topParent, parentObj: x, field: "Left"})
			walk(x.Right, scanParentRef{topParent: topParent, parentObj: x, field: "Right"})
		case *MultiHashJoin:
			for i, t := range x.Tables {
				walk(t, scanParentRef{topParent: topParent, mhParent: x, mhIndex: i})
			}
		case *Aggregate, *WindowAgg:
			return
		}
	}
	// Initial ref: target lives directly under topParent.Child.
	walk(n, scanParentRef{topParent: topParent})
	if collide || hit == nil {
		return nil, scanParentRef{}, false
	}
	return hit, hitRef, true
}

// replaceNodeAtParentSlot writes `newNode` into the parent slot
// described by `ref`, replacing `target`. Handles the four parent
// shapes: top-Filter.Child, generic-parent.Child / Left / Right
// for unary/binary nodes, and MultiHashJoin.Tables[i].
func replaceNodeAtParentSlot(ref scanParentRef, target *SeqScan, newNode Node) bool {
	// MultiHashJoin slot.
	if ref.mhParent != nil {
		if ref.mhIndex < 0 || ref.mhIndex >= len(ref.mhParent.Tables) {
			return false
		}
		if ref.mhParent.Tables[ref.mhIndex] != Node(target) {
			return false
		}
		ref.mhParent.Tables[ref.mhIndex] = newNode
		return true
	}
	// Top-Filter slot (target is the immediate child of the Filter
	// we are rewriting under).
	if ref.parentObj == nil {
		if ref.topParent != nil && ref.topParent.Child == Node(target) {
			ref.topParent.Child = newNode
			return true
		}
		return false
	}
	switch p := ref.parentObj.(type) {
	case *Filter:
		if ref.field == "Child" && p.Child == Node(target) {
			p.Child = newNode
			return true
		}
	case *Project:
		if ref.field == "Child" && p.Child == Node(target) {
			p.Child = newNode
			return true
		}
	case *Sort:
		if ref.field == "Child" && p.Child == Node(target) {
			p.Child = newNode
			return true
		}
	case *Limit:
		if ref.field == "Child" && p.Child == Node(target) {
			p.Child = newNode
			return true
		}
	case *Join:
		switch ref.field {
		case "Left":
			if p.Left == Node(target) {
				p.Left = newNode
				return true
			}
		case "Right":
			if p.Right == Node(target) {
				p.Right = newNode
				return true
			}
		}
	}
	return false
}

// splitPlannerAnd splits a planner.Expr AND-chain into leaf conjuncts.
func splitPlannerAnd(e Expr) []Expr {
	if e == nil {
		return nil
	}
	bop, ok := e.(*BinaryOp)
	if !ok || bop.Op != parser.OpAnd {
		return []Expr{e}
	}
	out := splitPlannerAnd(bop.Left)
	out = append(out, splitPlannerAnd(bop.Right)...)
	return out
}

// joinPlannerAnd reverses `splitPlannerAnd`.
func joinPlannerAnd(conjs []Expr) Expr {
	if len(conjs) == 0 {
		return nil
	}
	out := conjs[0]
	for _, c := range conjs[1:] {
		out = &BinaryOp{Op: parser.OpAnd, Left: out, Right: c}
	}
	return out
}

// --- MultiHashJoin-internal Filters absorption.

// rewriteMHJInputsWithSingleTablePredicates absorbs single-table
// constant-RHS conjuncts that landed in `mh.Filters` (rather than
// in a wrapping `*Filter`) into the appropriate `Tables[i]`. Same
// equality + range vocabulary as the Filter-walking pass above.
func rewriteMHJInputsWithSingleTablePredicates(mh *MultiHashJoin, cat catalog.Catalog) {
	if mh == nil || cat == nil || len(mh.Filters) == 0 {
		return
	}

	// Group conjuncts by (Tables-index, column). Same shape as the
	// Filter pass but without the unique-scan walk because the
	// scans are explicit in mh.Tables.
	type tabCol struct {
		idx int
		col string
	}
	type bounds struct {
		eqKey      Expr
		loKey      Expr
		hiKey      Expr
		eqConjunct Expr
	}
	groups := map[tabCol]*bounds{}
	for _, f := range mh.Filters {
		col, op, val, ok := matchSingleTableConstantPredicate(f)
		if !ok {
			continue
		}
		// Find the unique Tables[i] (still a SeqScan; previously-
		// rewritten ones are skipped) whose Output() contains col.Name.
		targetIdx := -1
		ambiguous := false
		for i, t := range mh.Tables {
			ss, ok := t.(*SeqScan)
			if !ok {
				continue
			}
			for _, sc := range ss.Output() {
				if sc.Name == col.Name {
					if targetIdx >= 0 {
						ambiguous = true
					}
					targetIdx = i
					break
				}
			}
			if ambiguous {
				break
			}
		}
		if ambiguous || targetIdx < 0 {
			continue
		}
		k := tabCol{idx: targetIdx, col: col.Name}
		b, ok := groups[k]
		if !ok {
			b = &bounds{}
			groups[k] = b
		}
		switch op {
		case parser.OpEq:
			if b.eqKey == nil {
				b.eqKey = val
				b.eqConjunct = f
			}
		case parser.OpGe, parser.OpGt:
			if b.loKey == nil {
				b.loKey = val
			}
		case parser.OpLe, parser.OpLt:
			if b.hiKey == nil {
				b.hiKey = val
			}
		}
	}

	// Rewrite each group: equality wins over range; only one
	// rewrite per Tables[i] (IndexScan has a single Index field).
	rewroteIdx := map[int]bool{}
	dropEq := make(map[Expr]struct{})
	// Stable order: equality first, then range, smaller idx first.
	for _, isEqualityPass := range []bool{true, false} {
		for k, b := range groups {
			if rewroteIdx[k.idx] {
				continue
			}
			isEq := b.eqKey != nil
			if isEq != isEqualityPass {
				continue
			}
			if !isEq && b.loKey == nil && b.hiKey == nil {
				continue
			}
			ss, ok := mh.Tables[k.idx].(*SeqScan)
			if !ok {
				continue
			}
			idx := findBTreeIndexForColumn(cat, ss.Table, k.col)
			if idx == nil {
				continue
			}
			if isEq {
				mh.Tables[k.idx] = &IndexScan{
					pos: ss.Pos(), Table: ss.Table, Alias: ss.Alias, Index: idx,
					Key: b.eqKey, schema: ss.Output(),
				}
			} else {
				mh.Tables[k.idx] = &IndexScan{
					pos: ss.Pos(), Table: ss.Table, Alias: ss.Alias, Index: idx,
					LowKey: b.loKey, HighKey: b.hiKey,
					schema: ss.Output(),
				}
			}
			rewroteIdx[k.idx] = true
			if isEq && b.eqConjunct != nil {
				dropEq[b.eqConjunct] = struct{}{}
			}
		}
	}

	// Rebuild mh.Filters dropping absorbed equality conjuncts.
	kept := mh.Filters[:0]
	for _, f := range mh.Filters {
		if _, drop := dropEq[f]; drop {
			continue
		}
		kept = append(kept, f)
	}
	mh.Filters = kept
}

// pushSingleSourceFiltersIntoMHJTables (M0071-0004) walks
// mh.Filters and pushes any conjunct whose every ColumnRef
// references a single Tables[i]'s column-range down onto a
// Filter wrapping that Tables[i]. Unlike
// rewriteMHJInputsWithSingleTablePredicates, this does NOT
// require an index to exist — non-equality conjuncts (e.g.
// `r_name = 'ASIA'` on Q5's region without an r_name index)
// still benefit because the build-side Tables[i] gets fewer
// rows before MHJ's hash-build / probe.
//
// Q3 regression guard: every conjunct that references columns
// from multiple Tables[i] (or no Tables[i]) is left in
// mh.Filters untouched. The push happens iff every ColumnRef
// in the conjunct lands in [tableOffset[i], tableOffset[i+1]).
//
// The conjunct's ColumnRef indices are SHIFTED to be local to
// Tables[i].Output() so the wrapped Filter evaluates against
// Tables[i]'s row directly.
func pushSingleSourceFiltersIntoMHJTables(mh *MultiHashJoin) {
	if mh == nil || len(mh.Filters) == 0 || len(mh.Tables) == 0 {
		return
	}
	// Compute cumulative offsets for each Tables[i].
	offsets := make([]int, len(mh.Tables)+1)
	for i, t := range mh.Tables {
		offsets[i+1] = offsets[i] + len(t.Output())
	}
	// For each conjunct, find the unique Tables[i] whose
	// [offsets[i], offsets[i+1]) range covers ALL ColumnRefs.
	tableForConjunct := func(c Expr) int {
		target := -1
		ok := true
		walkColumnRefs(c, func(idx int) {
			if !ok {
				return
			}
			t := -1
			for i := 0; i < len(mh.Tables); i++ {
				if idx >= offsets[i] && idx < offsets[i+1] {
					t = i
					break
				}
			}
			if t < 0 {
				ok = false
				return
			}
			if target < 0 {
				target = t
			} else if target != t {
				ok = false
			}
		}, func() { ok = false })
		if !ok {
			return -1
		}
		return target
	}
	// shiftColumnRefs adjusts every ColumnRef.Index in `e` by
	// `delta` (typically -offsets[i] to localise indices to
	// Tables[i]).
	var shiftColumnRefs func(Expr, int)
	shiftColumnRefs = func(e Expr, delta int) {
		if e == nil {
			return
		}
		switch x := e.(type) {
		case *ColumnRef:
			x.Index += delta
		case *BinaryOp:
			shiftColumnRefs(x.Left, delta)
			shiftColumnRefs(x.Right, delta)
		case *UnaryOp:
			shiftColumnRefs(x.Operand, delta)
		case *FuncCall:
			for _, a := range x.Args {
				shiftColumnRefs(a, delta)
			}
		}
	}
	kept := make([]Expr, 0, len(mh.Filters))
	for _, f := range mh.Filters {
		idx := tableForConjunct(f)
		if idx < 0 {
			kept = append(kept, f)
			continue
		}
		// Localise indices and wrap Tables[idx] in a Filter.
		// Clone the expr first so the shift doesn't mutate any
		// shared subtree (mh.Filters may share structure with
		// the enclosing Filter's predicate).
		local := cloneExprForShift(f)
		shiftColumnRefs(local, -offsets[idx])
		// Wrap Tables[idx]: if it's already a Filter, AND the
		// conjunct into the existing predicate; otherwise wrap.
		if existing, ok := mh.Tables[idx].(*Filter); ok {
			existing.Predicate = combineAnd([]Expr{existing.Predicate, local})
		} else {
			mh.Tables[idx] = &Filter{
				Child:     mh.Tables[idx],
				Predicate: local,
			}
		}
	}
	mh.Filters = kept
}

// cloneExprForShift makes a structural copy of an expression
// so index shifts on one branch don't propagate to other
// references in the plan tree. Only the ColumnRef-/operator-
// branches that pushSingleSourceFiltersIntoMHJTables walks
// are actually mutated, so the clone only needs to copy
// those.
func cloneExprForShift(e Expr) Expr {
	if e == nil {
		return nil
	}
	switch x := e.(type) {
	case *ColumnRef:
		cl := *x
		return &cl
	case *BinaryOp:
		return &BinaryOp{
			Op:    x.Op,
			Left:  cloneExprForShift(x.Left),
			Right: cloneExprForShift(x.Right),
		}
	case *UnaryOp:
		return &UnaryOp{Op: x.Op, Operand: cloneExprForShift(x.Operand)}
	case *FuncCall:
		args := make([]Expr, len(x.Args))
		for i, a := range x.Args {
			args[i] = cloneExprForShift(a)
		}
		return &FuncCall{Name: x.Name, Args: args, Star: x.Star}
	default:
		return e
	}
}
