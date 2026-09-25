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
// goopg still evaluates the qual as a Filter — for a monotonic function it
// removes exactly the rows the run condition stops at — so only the estimate
// follows PG here. The executor's early stop and EXPLAIN's `Run Condition:`
// line are not ported (ledgered).

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
