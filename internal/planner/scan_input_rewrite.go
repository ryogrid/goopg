package planner

import "github.com/goopg/goopg/internal/parser"

// Single-table predicate routing into scan inputs (M0054-0006a-pre).
//
// The M0054-0002 EXPLAIN baseline showed several queries doing
// `Seq Scan` on tables that DO have a usable B-tree index when the
// constant-RHS predicate sat in a `*Filter` wrapper above an
// outer-binary HashJoin (or, until M0127-P6.2, in a
// `*MultiHashJoin.Filters` slice). The existing index-selection code (`planIndexScanFromWhere` /
// `tryRangeIndexScan`) only fires for single-table SELECTs at
// `planSelect` time — once the bushy join tree is built, no pass
// re-runs index selection on the inputs.
//
// This file adds a generic post-pass that walks the plan tree once
// after join-order search, and for every `*Filter` it finds, groups
// single-table constant-RHS conjuncts by (target SeqScan, column) and replaces
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

import "github.com/goopg/goopg/internal/catalog"

// rewriteScanInputsWithSingleTablePredicates is the entry point.
// Mutates the plan tree in place and returns the (possibly
// substituted) root.
func rewriteScanInputsWithSingleTablePredicates(n Node, cat catalog.Catalog) Node {
	if n == nil || cat == nil {
		return n
	}
	// M0127-P5.9-b (08 §3): a searched subtree keeps its own leaves. This pass
	// absorbs conjuncts DOWNWARD out of a `*Filter` into the scan below it,
	// addressing both in FROM-cumulative coordinates; inside a searched tree
	// the leaf's quals are already leaf-local (`LeafLocal`, attached before the
	// search ran) and the joins between are not in that space at all.
	if isSearchedTree(n) {
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
//
// It carried an `mhParent *MultiHashJoin` + `mhIndex int` variant until
// M0127-P6.2, because `MultiHashJoin.Tables` was a slice slot rather than a
// named field; every surviving parent shape names its slot, so `field` alone
// locates it.
type scanParentRef struct {
	topParent *Filter
	parentObj any
	field     string
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
				Key: ch.bounds.eqKey, schema: ss.Output(), SmallDim: ss.SmallDim, UniqueKeys: ss.UniqueKeys,
			}
		} else {
			newScan = &IndexScan{
				pos: ss.Pos(), Table: ss.Table, Alias: ss.Alias, Index: idx,
				LowKey: ch.bounds.loKey, HighKey: ch.bounds.hiKey,
				schema: ss.Output(), SmallDim: ss.SmallDim, UniqueKeys: ss.UniqueKeys,
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
	case *IntegerConst, *NumericConst, *StringConst, *TypedStringLit, *ParamRef, *OuterColumnRef:
		// OuterColumnRef: correlated subquery probe key resolved from ctx.OuterRows at runtime.
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
// boundary); join subtrees ARE descended, since an outer Filter
// wrapping a join tree is the case M0054-0006a-pre is meant to handle
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
// described by `ref`, replacing `target`. Handles the three parent shapes:
// top-Filter.Child and generic-parent.Child / Left / Right for unary and
// binary nodes. (A `MultiHashJoin.Tables[i]` slot was the fourth until
// M0127-P6.2.)
func replaceNodeAtParentSlot(ref scanParentRef, target *SeqScan, newNode Node) bool {
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

// ---------------------------------------------------------------------------
// The MultiHashJoin half of this file — `rewriteMHJInputsWithSingleTablePredicates`
// (absorb single-table constant-RHS conjuncts out of `mh.Filters` into the
// matching `Tables[i]`), `pushSingleSourceFiltersAfterRemap` /
// `pushSingleSourceFiltersIntoMHJTables` (M0071-0004 + RC-1b: push a conjunct
// whose every ColumnRef lands in ONE `Tables[i]`'s column range down onto that
// table, no index required) and their `cloneExprForShift` helper — was deleted
// with the node by M0127-P6.2 (08 §4).
//
// Two of its findings govern the passes that remain. First, RC-1b's ordering
// rule: a pass that attributes a conjunct to an input BY COLUMN-INDEX RANGE
// must run after the layout remap, or it attributes FROM-cumulative indices
// against post-rewrite offsets and pushes the predicate onto the WRONG scan —
// a TPC-DS Q47 date_dim OR-predicate landed on `store` and silently zeroed the
// result. Second, the defence that made that failure recoverable: validate
// every ref POSITIONALLY BY NAME (the column at the index-derived slot must
// carry the name the ref claims) and decline the push on any mismatch, so a
// path where the remap did not run degrades to post-join evaluation — slower,
// never wrong. `pushSingleSideQualsIntoInnerJoinInputs` is the surviving
// binary-Join expression of both rules.
