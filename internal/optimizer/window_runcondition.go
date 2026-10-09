package optimizer

import (
	"strings"

	"github.com/goopg/goopg/internal/parser"
)

// Window run conditions (M0146-0005i).
//
// PG's set_subquery_pathlist offers every outer qual on a subquery's window
// function output to check_and_push_window_quals
// (postgres/src/backend/optimizer/path/allpaths.c). When the function is
// monotonic (its prosupport answers SupportRequestWFuncMonotonic) and the qual
// has the matching direction, find_window_run_conditions turns it into the
// WindowAgg's runCondition and sets keep_original = false: the qual leaves
// the subquery rel's baserestrictinfo, so it contributes NO selectivity to
// the rel's row estimate. TPC-DS Q44's `rnk < 11` over `rank() OVER (…)`
// keeps PG's subquery at 5424 rows where goopg's default 1/3 inequality gave
// 1831.
//
// M0146-0005dn: pushWindowRunConditions moves such a qual off its Filter
// and onto the top-level WindowAgg's RunCondition at Plan()'s tail, the
// executor stops the partition there, and EXPLAIN prints `Run Condition:`.

// monotonicity is SupportRequestWFuncMonotonic's answer.
type monotonicity uint8

const (
	monoIncreasing monotonicity = 1 << iota
	monoDecreasing
	monoBoth = monoIncreasing | monoDecreasing
)

// windowRunConditionDropsQual reports whether PG would push conjunct `c`,
// evaluated over `child`'s output, into a WindowAgg run condition AND drop the
// original qual (keep_original = false). Like check_and_push_window_quals, it
// looks at the left operand first, then the right one; the other operand must
// be pseudo-constant, and the window-function side must be a plain column
// reference to a window function's result.
func windowRunConditionDropsQual(c Expr, child Node) bool {
	b, ok := c.(*BinaryOp)
	if !ok {
		return false
	}
	switch b.Op {
	case parser.OpLt, parser.OpLe, parser.OpGt, parser.OpGe, parser.OpEq:
	default:
		return false
	}
	if cr, ok := b.Left.(*ColumnRef); ok && isConstantExpr(b.Right) {
		if m, ok := windowFuncMonotonicity(child, cr.Index); ok {
			return runConditionDropsOriginal(b.Op, true, m)
		}
	}
	if cr, ok := b.Right.(*ColumnRef); ok && isConstantExpr(b.Left) {
		if m, ok := windowFuncMonotonicity(child, cr.Index); ok {
			return runConditionDropsOriginal(b.Op, false, m)
		}
	}
	return false
}

// runConditionDropsOriginal is find_window_run_conditions' operator switch:
// `<`/`<=` need an increasing function on the left (or a decreasing one on
// the right), `>`/`>=` the reverse, and `=` drops the original only when the
// function is both (a constant over the partition); otherwise `=` becomes a
// `<=`/`>=` run condition but keeps its qual.
func runConditionDropsOriginal(op parser.OpCode, wfuncLeft bool, m monotonicity) bool {
	switch op {
	case parser.OpLt, parser.OpLe:
		return (wfuncLeft && m&monoIncreasing != 0) || (!wfuncLeft && m&monoDecreasing != 0)
	case parser.OpGt, parser.OpGe:
		return (wfuncLeft && m&monoDecreasing != 0) || (!wfuncLeft && m&monoIncreasing != 0)
	case parser.OpEq:
		return m == monoBoth
	}
	return false
}

// windowFuncMonotonicity resolves output column `idx` of `n` to a window
// function result, through identity Project columns, Sorts and stacked
// WindowAggs, and returns the function's monotonicity. ok is false when the
// column is not a bare window function result or the function has no
// monotonic support.
func windowFuncMonotonicity(n Node, idx int) (monotonicity, bool) {
	for n != nil {
		switch x := n.(type) {
		case *Project:
			if idx < 0 || idx >= len(x.Targets) {
				return 0, false
			}
			cr, ok := x.Targets[idx].(*ColumnRef)
			if !ok {
				return 0, false
			}
			n, idx = x.Child, cr.Index
		case *Sort:
			n = x.Child
		case *SubqueryScan:
			n = x.Child
		case *WindowAgg:
			base := len(x.schema) - len(x.Funcs)
			if idx < base {
				n = x.Child
				continue
			}
			if idx-base >= len(x.Funcs) {
				return 0, false
			}
			return windowFuncSupportMonotonic(x.Funcs[idx-base], x)
		default:
			return 0, false
		}
	}
	return 0, false
}

// windowFuncSupportMonotonic is the prosupport answer for the functions PG
// gives one (postgres/src/backend/utils/adt/windowfuncs.c window_*_support,
// and int8inc_support in int8.c for count): row_number, rank, dense_rank,
// percent_rank, cume_dist and ntile are always increasing; count is both
// without ORDER BY (every row is a peer), increasing when the frame starts
// at UNBOUNDED PRECEDING and decreasing when it ends at UNBOUNDED FOLLOWING.
func windowFuncSupportMonotonic(f WindowFunc, w *WindowAgg) (monotonicity, bool) {
	switch strings.ToLower(f.Name) {
	case "row_number", "rank", "dense_rank", "percent_rank", "cume_dist", "ntile":
		return monoIncreasing, true
	case "count":
		if len(w.OrderBy) == 0 {
			return monoBoth, true
		}
		// A nil frame is the default RANGE UNBOUNDED PRECEDING .. CURRENT ROW.
		start, end := parser.FrameBoundUnboundedPreceding, parser.FrameBoundCurrentRow
		if w.Frame != nil {
			start, end = w.Frame.StartKind, w.Frame.EndKind
		}
		var m monotonicity
		if start == parser.FrameBoundUnboundedPreceding {
			m |= monoIncreasing
		}
		if end == parser.FrameBoundUnboundedFollowing {
			m |= monoDecreasing
		}
		return m, m != 0
	}
	return 0, false
}

// dropWindowRunConditions returns `pred` without the conjuncts PG turns into
// run conditions with keep_original = false (nil when none remain), for the
// selectivity estimators that score a qual over `child`.
func dropWindowRunConditions(pred Expr, child Node) Expr {
	if pred == nil || child == nil {
		return pred
	}
	conjuncts := splitAnd(pred)
	kept := conjuncts[:0:0]
	for _, c := range conjuncts {
		if !windowRunConditionDropsQual(c, child) {
			kept = append(kept, c)
		}
	}
	if len(kept) == len(conjuncts) {
		return pred
	}
	return combineAnd(kept)
}

// pushWindowRunConditions is find_window_run_conditions' plan half
// (M0146-0005dn): every Filter conjunct that PG turns into a WindowAgg run
// condition with keep_original = false leaves the Filter and becomes the
// RunCondition of the top-level WindowAgg it reads, and a Filter left with
// no conjunct disappears (TPC-DS Q44's `Subquery Scan on v11` then carries
// no `Filter: (v11.rnk < 11)`; its WindowAgg prints `Run Condition: (rank()
// OVER w1 < 11)`).
//
// Only a function of the FIRST WindowAgg below the Filter qualifies: PG
// also accepts one of a lower WindowAgg in the same query level, keeping
// the rows flowing to the windows above in non-strict pass-through and
// filtering them at the top (create_one_window_path's topqual) — not
// ported. A `=` comparison on a function monotonic in one direction only
// becomes a `<=` / `>=` run condition while its qual stays on the Filter
// (keep_original = true), as PG does.
func pushWindowRunConditions(n Node) Node {
	// mapPlanChildren copies every node it passes, which would split a
	// subtree several parents share (a CTE body read twice) into copies;
	// rebuild only when some Filter actually carries a run condition.
	if !hasWindowRunConditionCandidate(n, 0) {
		return n
	}
	return rebuildWindowRunConditions(n)
}

// hasWindowRunConditionCandidate is the read-only scan for a Filter with a
// conjunct pushWindowRunConditions would move.
func hasWindowRunConditionCandidate(n Node, depth int) bool {
	if n == nil || depth > 256 {
		return false
	}
	if f, ok := n.(*Filter); ok && f.Predicate != nil {
		for _, c := range splitAnd(f.Predicate) {
			if _, _, _, ok := windowRunConditionFor(c, f.Child); ok {
				return true
			}
		}
	}
	kids, _ := planChildNodes(n)
	for _, c := range kids {
		if hasWindowRunConditionCandidate(c, depth+1) {
			return true
		}
	}
	return false
}

func rebuildWindowRunConditions(n Node) Node {
	if n == nil {
		return nil
	}
	n = mapPlanChildren(n, rebuildWindowRunConditions)
	f, ok := n.(*Filter)
	if !ok || f.Predicate == nil {
		return n
	}
	conjuncts := splitAnd(f.Predicate)
	kept := conjuncts[:0:0]
	moved := false
	for _, c := range conjuncts {
		if wa, rc, keep, ok := windowRunConditionFor(c, f.Child); ok {
			// Plan() can reach a subquery's tree twice (its own call and the
			// enclosing one), and a kept `=` qual is offered again: add each
			// run condition once.
			present := false
			for _, have := range splitAnd(wa.RunCondition) {
				if exprEqual(have, rc) {
					present = true
				}
			}
			switch {
			case present:
			case wa.RunCondition == nil:
				wa.RunCondition = rc
			default:
				wa.RunCondition = combineAnd([]Expr{wa.RunCondition, rc})
			}
			if !keep {
				moved = true
				continue
			}
		}
		kept = append(kept, c)
	}
	if !moved {
		return n
	}
	if len(kept) == 0 {
		return f.Child
	}
	nf := *f
	nf.Predicate = combineAnd(kept)
	return &nf
}

// windowRunConditionFor returns the top-level WindowAgg under `child` and
// conjunct `c` rewritten as a run condition over that WindowAgg's own
// output, and whether the original qual must stay (keep_original).
func windowRunConditionFor(c Expr, child Node) (*WindowAgg, Expr, bool, bool) {
	b, ok := c.(*BinaryOp)
	if !ok {
		return nil, nil, false, false
	}
	wfuncLeft := true
	cr, ok := b.Left.(*ColumnRef)
	if !ok || !isConstantExpr(b.Right) {
		wfuncLeft = false
		cr, ok = b.Right.(*ColumnRef)
		if !ok || !isConstantExpr(b.Left) {
			return nil, nil, false, false
		}
	}
	m, ok := windowFuncMonotonicity(child, cr.Index)
	if !ok {
		return nil, nil, false, false
	}
	op, keep := b.Op, false
	switch b.Op {
	case parser.OpLt, parser.OpLe, parser.OpGt, parser.OpGe:
		if !runConditionDropsOriginal(b.Op, wfuncLeft, m) {
			return nil, nil, false, false
		}
	case parser.OpEq:
		if m != monoBoth {
			// find_window_run_conditions' `=` arm: filter out the values
			// past the constant and keep the equality itself.
			keep = true
			op = parser.OpLe
			if (m&monoIncreasing != 0) != wfuncLeft {
				op = parser.OpGe
			}
		}
	default:
		return nil, nil, false, false
	}
	wa, waIdx, ok := topWindowAggColumn(child, cr.Index)
	if !ok {
		return nil, nil, false, false
	}
	rc, ok := CloneExprReplacingColumnRefs(c, func(x *ColumnRef) Expr {
		return &ColumnRef{Index: waIdx, Name: x.Name, Type: x.Type}
	})
	if !ok {
		return nil, nil, false, false
	}
	if rb, isBin := rc.(*BinaryOp); isBin && op != b.Op {
		nb := *rb
		nb.Op = op
		rc = &nb
	}
	return wa, rc, keep, true
}

// topWindowAggColumn follows output column `idx` of `n` through identity
// SubqueryScan / Project columns and Sorts to the first WindowAgg, and
// returns it with the column's index in its output, when that column is one
// of the WindowAgg's own functions.
func topWindowAggColumn(n Node, idx int) (*WindowAgg, int, bool) {
	for n != nil {
		switch x := n.(type) {
		case *SubqueryScan:
			n = x.Child
		case *Project:
			if idx < 0 || idx >= len(x.Targets) {
				return nil, 0, false
			}
			cr, ok := x.Targets[idx].(*ColumnRef)
			if !ok {
				return nil, 0, false
			}
			n, idx = x.Child, cr.Index
		case *Sort:
			n = x.Child
		case *WindowAgg:
			base := len(x.schema) - len(x.Funcs)
			if idx < base || idx >= len(x.schema) {
				return nil, 0, false
			}
			return x, idx, true
		default:
			return nil, 0, false
		}
	}
	return nil, 0, false
}
