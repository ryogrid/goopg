package executor

import (
	"github.com/goopg/goopg/internal/optimizer"
)

// indexReadSetsDisabled is the A/B and equivalence-test knob for the
// index-scan read sets (M0122-0012 C34): when set, no statement publishes
// them and every index-scan heap fetch decodes its whole deform window.
var indexReadSetsDisabled = false

// indexReadSets maps each IndexScan of a statement's plan to the table
// columns its fetched rows may be read at. An entry's nil value, and a
// missing entry, both mean every column.
type indexReadSets map[*optimizer.IndexScan][]bool

// computeIndexReadSets walks a statement's plan top-down with the set of
// output columns each node's parent may read — the set-valued twin of the
// integer deform bound (deformBoundBelow / deformJoinBounds) — and records
// the set reaching every IndexScan it gets to (M0122-0012 C34).
//
// PG fetches only the attributes a node's expressions reference
// (EEOP_SCAN_FETCHSOME up to last_scan, ExecCreateExprSetupSteps in
// execExpr.c) and leaves a by-reference attribute as a pointer. goopg
// converts every decoded value, so an index scan that decodes columns
// nothing reads pays for each one; the seq scan already skips them (slice
// 19, colTypeInfo.skip), the index scan could not, because the read set of
// an index scan under a lateral join, a SetOp or a Gather worker never
// reached its build.
//
// The walk is fail-closed. A node kind it does not model stops it: the
// IndexScans below get no entry and decode in full. Inside a modelled node an
// expression consumerLiveColumns cannot enumerate widens the child's set to
// every column. A set only ever grows on the way down, so a column is skipped
// only when no node between the root and the scan may read it.
func computeIndexReadSets(root optimizer.Node) indexReadSets {
	if indexReadSetsDisabled || root == nil {
		return nil
	}
	w := indexReadSetWalk{sets: indexReadSets{}}
	w.walk(root, nil)
	if len(w.sets) == 0 {
		return nil
	}
	return w.sets
}

type indexReadSetWalk struct {
	sets indexReadSets
}

// readSetAll reports whether a set means every column.
func readSetAll(s []bool) bool { return s == nil }

// readSetUnion is a ∪ b; nil (every column) absorbs.
func readSetUnion(a, b []bool) []bool {
	if a == nil || b == nil {
		return nil
	}
	out := make([]bool, max(len(a), len(b)))
	for i := range out {
		out[i] = (i < len(a) && a[i]) || (i < len(b) && b[i])
	}
	return out
}

// readSetAdd is need ∪ the columns exprs read (nil when they cannot be
// enumerated).
func readSetAdd(need []bool, exprs ...optimizer.Expr) []bool {
	if need == nil {
		return nil
	}
	return readSetUnion(need, exprReadSet(exprs))
}

// readSetSlice is s[from:to) re-based at 0, or nil when s is nil.
func readSetSlice(s []bool, from, to int) []bool {
	if s == nil {
		return nil
	}
	out := make([]bool, to-from)
	for i := from; i < to && i < len(s); i++ {
		out[i-from] = s[i]
	}
	return out
}

func (w *indexReadSetWalk) walk(n optimizer.Node, need []bool) {
	switch p := n.(type) {
	case *optimizer.IndexScan:
		w.record(p, need)
	case *optimizer.Project:
		// The projection evaluates the targets its parent reads, plus any
		// it cannot skip (projectLiveSet keeps every target that might fail
		// or have an effect); those are its child's only readers.
		var child []bool
		if p.IsolatedScope {
			child = nil
		} else {
			var targets []optimizer.Expr
			for i, t := range p.Targets {
				if readSetAll(need) || (i < len(need) && need[i]) || !projectTargetSkippable(t) {
					targets = append(targets, t)
				}
			}
			if !readSetAll(need) && len(need) > len(p.Targets) {
				for _, b := range need[len(p.Targets):] {
					if b {
						targets = p.Targets
						break
					}
				}
			}
			child = exprReadSet(targets)
		}
		w.walk(p.Child, child)
	case *optimizer.Filter:
		w.walk(p.Child, readSetAdd(need, p.Predicate))
	case *optimizer.Sort:
		w.walk(p.Child, readSetAdd(need, deformSortKeyExprs(p.Keys)...))
	case *optimizer.IncrementalSort:
		w.walk(p.Child, readSetAdd(need, deformSortKeyExprs(p.Keys)...))
	case *optimizer.Limit:
		exprs := append([]optimizer.Expr{p.Limit, p.Offset}, p.TiesKeys...)
		w.walk(p.Child, readSetAdd(need, exprs...))
	case *optimizer.Gather:
		w.walk(p.Child, need)
	case *optimizer.GatherMerge:
		w.walk(p.Child, readSetAdd(need, deformSortKeyExprs(p.Keys)...))
	case *optimizer.SubqueryScan:
		// Labelling pass-through: output column i is child column i.
		w.walk(p.Child, need)
	case *optimizer.Materialize:
		w.walk(p.Child, need)
	case *optimizer.Memoize:
		// The cache returns the child's rows as they are; its keys are read
		// from the outer row (the lateral join accounts for them).
		w.walk(p.Child, need)
	case *optimizer.Aggregate:
		// Reshape boundary: the arms are the only readers of the child's
		// rows, as for buildCompleteChild. A Finalize aggregate reads its
		// child's state columns without an arm.
		if p.Mode == optimizer.AggModeFinal {
			w.walk(p.Child, nil)
			return
		}
		w.walk(p.Child, exprReadSet(aggConsumerExprs(p)))
	case *optimizer.SetOp:
		// A set operation compares and emits whole input rows.
		w.walk(p.Left, nil)
		w.walk(p.Right, nil)
	case *optimizer.DistinctOn:
		// Deduplicates whole rows by its keys and emits them: read in full.
		w.walk(p.Child, nil)
	case *optimizer.Distinct:
		w.walk(p.Child, nil)
	case *optimizer.CTEScan:
		// The CTE body is materialised once for every reference.
		w.walk(p.Child, nil)
	case *optimizer.Join:
		left, right := w.joinSides(p, need)
		w.walk(p.Left, left)
		w.walk(p.Right, right)
	case *optimizer.NestedLoopIndexJoin:
		// The inner probe is not modelled: it decodes in full.
		w.walk(p.Outer, nil)
	}
	// Any other node stops the walk: the IndexScans below it get no entry.
}

// joinSides splits a join's output read set over its sides. The layouts
// follow deformJoinBounds: an inner/outer/cross join emits left++right, the
// equi-key pairs are evaluated positionally (k.Left on left rows, k.Right on
// right rows), and the residual reads the merged row. A lateral join's inner
// also reads the outer row through OuterColumnRef{Level: 1}; those reads are
// added to the left side (lateralOuterReadSet), or the left side is read in
// full when the inner's reads cannot be enumerated.
func (w *indexReadSetWalk) joinSides(p *optimizer.Join, need []bool) (left, right []bool) {
	switch p.Type {
	case optimizer.JoinTypeInner, optimizer.JoinTypeLeft, optimizer.JoinTypeRight,
		optimizer.JoinTypeFull, optimizer.JoinTypeCross:
	default:
		return nil, nil
	}
	if p.NullAware || len(p.UsingLeftCols) > 0 || len(p.UsingRightCols) > 0 {
		return nil, nil
	}
	leftW, rightW := deformSideWidth(p.Left), deformSideWidth(p.Right)
	if leftW <= 0 || rightW <= 0 || len(p.Output()) != leftW+rightW {
		return nil, nil
	}
	if need != nil {
		for i := leftW + rightW; i < len(need); i++ {
			if need[i] {
				return nil, nil
			}
		}
	}
	pred := exprReadSet([]optimizer.Expr{p.Predicate})
	if pred == nil {
		return nil, nil
	}
	left = readSetUnion(readSetSlice(need, 0, leftW), readSetSlice(pred, 0, leftW))
	right = readSetUnion(readSetSlice(need, leftW, leftW+rightW), readSetSlice(pred, leftW, leftW+rightW))
	var lk, rk []optimizer.Expr
	if len(p.HashKeys) > 0 {
		for _, k := range p.HashKeys {
			lk = append(lk, k.Left)
			rk = append(rk, k.Right)
		}
	} else {
		lk = append(lk, p.LeftKey)
		rk = append(rk, p.RightKey)
	}
	left = readSetFit(readSetAdd(left, lk...), leftW)
	right = readSetFit(readSetAdd(right, rk...), rightW)
	if p.Lateral && left != nil {
		left = readSetFit(readSetUnion(left, lateralOuterReadSet(p.Right)), leftW)
	}
	return left, right
}

// readSetFit returns s, or nil when s marks a column at or past width (a
// reference the side's layout does not account for).
func readSetFit(s []bool, width int) []bool {
	for i := width; i < len(s); i++ {
		if s[i] {
			return nil
		}
	}
	return s
}

// lateralOuterReadSet is the set of outer-row columns a lateral join's inner
// reads — deformLateralOuterRefs' set-valued twin. lateralJoinStream binds
// every outer row into the inner's outer slot; the probe's key expressions,
// a Memoize's cache keys and the probe's Cond read it as OuterColumnRef{Level:
// 1}, and a ColumnRef among the keys indexes the outer row too. Any other
// inner shape, any other outer reference, or an expression the walk cannot
// enumerate returns nil: the outer row is read in full.
func lateralOuterReadSet(right optimizer.Node) []bool {
	var keys, conds []optimizer.Expr
	for {
		m, ok := right.(*optimizer.Memoize)
		if !ok {
			break
		}
		keys = append(keys, m.KeyExprs...)
		right = m.Child
	}
	switch in := right.(type) {
	case *optimizer.IndexScan:
		keys = append(keys, deformNLIOuterKeyExprs(in)...)
		keys = append(keys, in.SAOPKeys...)
		keys = append(keys, in.RangePrefix...)
		conds = append(conds, in.Cond)
	case *optimizer.IndexOnlyScan:
		keys = append(keys, deformNLIOuterKeyExprs(in)...)
		conds = append(conds, in.Cond)
	default:
		return nil
	}
	read := []bool{}
	mark := func(idx int) bool {
		if idx < 0 {
			return false
		}
		for len(read) <= idx {
			read = append(read, false)
		}
		read[idx] = true
		return true
	}
	visit := func(e optimizer.Expr, isKey bool) bool {
		if e == nil {
			return true
		}
		known := true
		ok := optimizer.WalkExprHostScope(e, func(x optimizer.Expr) {
			switch r := x.(type) {
			case *optimizer.OuterColumnRef:
				if r.Level != 1 || !mark(r.Index) {
					known = false
				}
			case *optimizer.ColumnRef:
				// A key's ColumnRef indexes the outer row; a Cond's
				// indexes the inner row.
				if isKey && !mark(r.Index) {
					known = false
				}
			case *optimizer.SubqueryExpr:
				if !r.IsNonCorrelated || len(r.ParParam) > 0 {
					known = false
				}
			case *optimizer.MergeWholeRowRef, *optimizer.CTIDExpr, *optimizer.TableOidExpr:
				known = false
			}
		})
		return ok && known
	}
	for _, k := range keys {
		if !visit(k, true) {
			return nil
		}
	}
	for _, c := range conds {
		if !visit(c, false) {
			return nil
		}
	}
	return read
}

// record files need for an IndexScan leaf, unioned with its own Cond and
// its index's key columns (the Cond is evaluated against the fetched row;
// the key columns are kept for any recheck). A scan reached twice keeps the
// union.
func (w *indexReadSetWalk) record(p *optimizer.IndexScan, need []bool) {
	if p.Table == nil {
		return
	}
	width := len(p.Table.Columns)
	set := readSetAdd(need, p.Cond)
	if set != nil && p.Index != nil {
		for _, kc := range p.Index.Columns {
			for j, tc := range p.Table.Columns {
				if tc.Name == kc {
					set = readSetUnion(set, readSetMark(j))
					break
				}
			}
		}
	}
	set = readSetFit(set, width)
	if prev, ok := w.sets[p]; ok {
		set = readSetUnion(prev, set)
	}
	w.sets[p] = set
}

// exprReadSet is consumerLiveColumns that also accepts an uncorrelated
// sublink: its plan reads no outer value, so it reads nothing of the row
// (deformSublinkArgs, M0122-0012 slice 53 — PG plans it as an InitPlan).
// TPC-DS Q14's date_dim probes filter on `d_week_seq = (SELECT ...)`.
func exprReadSet(exprs []optimizer.Expr) []bool {
	live := []bool{}
	for _, e := range exprs {
		if e == nil {
			continue
		}
		known := true
		ok := optimizer.WalkExprHostScope(e, func(x optimizer.Expr) {
			switch r := x.(type) {
			case *optimizer.ColumnRef:
				if r.Index < 0 {
					known = false
					return
				}
				for len(live) <= r.Index {
					live = append(live, false)
				}
				live[r.Index] = true
			case *optimizer.SubqueryExpr:
				if !r.IsNonCorrelated || len(r.ParParam) > 0 {
					known = false
				}
			case *optimizer.MergeWholeRowRef, *optimizer.CTIDExpr, *optimizer.TableOidExpr:
				known = false
			}
		})
		if !ok || !known {
			return nil
		}
	}
	return live
}

// readSetMark is the set holding column i alone.
func readSetMark(i int) []bool {
	s := make([]bool, i+1)
	s[i] = true
	return s
}

// applyIndexReadSet marks the columns of info outside the scan's read set
// skip — the index-scan twin of seqScanOp.skipReadSet's application.
func applyIndexReadSet(ctx *Context, plan *optimizer.IndexScan, info []colTypeInfo) {
	if ctx == nil || ctx.indexReadSets == nil {
		return
	}
	set := ctx.indexReadSets[plan]
	if set == nil {
		return
	}
	skipped := false
	for i := range info {
		if i >= len(set) || !set[i] {
			info[i].skip = true
			skipped = true
		}
	}
	if skipped {
		indexReadSetApplied()
	}
}

// indexReadSetApplied is a test hook: tests count the probes that skipped
// columns, proving the read sets reached them.
var indexReadSetApplied = func() {}
