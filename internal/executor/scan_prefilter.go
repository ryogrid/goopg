package executor

import (
	"github.com/goopg/goopg/internal/optimizer"
)

// scanPrefilter lets a sequential scan apply the WHERE predicate of the Filter
// directly above it BEFORE the row is fully deformed and materialised.
//
// The shape is PostgreSQL's: SeqNext hands ExecScan a slot that still points
// into the shared buffer, ExecQual runs against it, and only a surviving tuple
// is deformed further and copied out. goopg previously deformed all 16
// lineitem columns and deep-copied every one of the 6 M rows TPC-H Q6 scans,
// to hand 98 % of them to a filterOp that threw them away.
//
// Two properties make this safe rather than merely fast:
//
//  1. **It can only remove rows the Filter above would remove anyway.** The
//     filterOp is left in place and still evaluates the same predicate on the
//     rows that survive, so this is a pure pre-rejection. A prefilter that
//     wrongly said "false" would be a bug; one that wrongly said "true" costs
//     nothing but time.
//  2. **The partially-deformed row never escapes the scan.** MaxCols is the
//     number of leading columns the predicate itself reads, and the partial
//     row is passed to nothing but that predicate. Anything that survives is
//     deformed the rest of the way before it is yielded.
//
// Because the predicate is evaluated twice on surviving rows, it must be
// deterministic and side-effect-free. That is enforced by a WHITELIST in
// prefilterSafeExpr: an expression node the whitelist does not name disables
// the prefilter entirely. The failure direction matters — this codebase has a
// documented history of expression walkers that silently miss an arm
// (`internal/optimizer/exprwalk_inventory_test.go` tracks the pending ones),
// and a missed arm here must cost performance, never correctness.
type scanPrefilter struct {
	pred optimizer.Expr
	// MaxCols is the exclusive upper bound on the column indexes pred reads:
	// the scan deforms cols[0:MaxCols] before evaluating it.
	MaxCols int
}

// planScanPrefilter decides whether pred can be evaluated inside the scan and,
// if so, how many leading columns it needs. ok=false means "leave the scan
// exactly as it was".
func planScanPrefilter(pred optimizer.Expr, ncols int) (scanPrefilter, bool) {
	if pred == nil || ncols <= 0 {
		return scanPrefilter{}, false
	}
	maxIdx := -1
	if !prefilterSafeExpr(pred, &maxIdx) {
		return scanPrefilter{}, false
	}
	// A predicate that reads no column at all (a constant) is not worth the
	// machinery and would also make the "partial row" empty.
	if maxIdx < 0 || maxIdx >= ncols {
		return scanPrefilter{}, false
	}
	need := maxIdx + 1
	// Nothing is saved when the predicate already reads the whole row; the
	// prefilter would then be a pure second evaluation.
	if need >= ncols {
		return scanPrefilter{}, false
	}
	return scanPrefilter{pred: pred, MaxCols: need}, true
}

// prefilterSafeExpr reports whether e is deterministic, free of side effects,
// free of subquery/outer-row dependencies, and reads only columns of the
// scanned row — recording the highest column index it reads in *maxIdx.
//
// This is deliberately a whitelist. Notably EXCLUDED, and why:
//
//   - FuncCall — overloads are not resolved here and volatility is unknown, so
//     a second evaluation could observe a different value (random(), now() in
//     a volatile spelling) or repeat a side effect.
//   - SubqueryExpr / ExistsExpr / InExpr / ArraySubqueryExpr — these open a
//     plan; running one per scanned tuple ahead of the filter would be slower,
//     not faster, and re-running it is a semantic risk.
//   - OuterColumnRef / ParamRef / ExecParamRef — depend on state the scan does
//     not own at this point in Next.
//   - CTIDExpr / TableOidExpr / MergeWholeRowRef / RowExpr — read the tuple or
//     row as a whole rather than named columns, so MaxCols cannot bound them.
//   - LikeEscapePattern / CollateExpr / CaseExpr / ExtractExpr — plausible but
//     unnecessary for the workloads this targets; left out rather than
//     reasoned about loosely.
func prefilterSafeExpr(e optimizer.Expr, maxIdx *int) bool {
	switch x := e.(type) {
	case nil:
		return false

	case *optimizer.ColumnRef:
		// A whole-row or array-typed reference is still a plain column read;
		// the index is what bounds the deform.
		if x.Index < 0 {
			return false
		}
		if x.Index > *maxIdx {
			*maxIdx = x.Index
		}
		return true

	// Literals: constant, no side effects, identical on re-evaluation.
	case *optimizer.IntegerConst, *optimizer.StringConst, *optimizer.NumericConst,
		*optimizer.TypedStringLit, *optimizer.IntervalLit, *optimizer.NullConst,
		*optimizer.BooleanConst:
		return true

	case *optimizer.BinaryOp:
		// Covers the comparison, arithmetic and AND/OR spellings. LIKE with an
		// ESCAPE clause carries a LikeEscapePattern on the right, which the
		// whitelist rejects, so those predicates simply opt out.
		return prefilterSafeExpr(x.Left, maxIdx) && prefilterSafeExpr(x.Right, maxIdx)

	case *optimizer.UnaryOp:
		return prefilterSafeExpr(x.Operand, maxIdx)

	case *optimizer.CastExpr:
		return prefilterSafeExpr(x.Operand, maxIdx)

	case *optimizer.IsNullExpr:
		return prefilterSafeExpr(x.Operand, maxIdx)

	case *optimizer.IsBoolExpr:
		return prefilterSafeExpr(x.Operand, maxIdx)

	case *optimizer.IsDistinctFromExpr:
		return prefilterSafeExpr(x.Left, maxIdx) && prefilterSafeExpr(x.Right, maxIdx)

	default:
		return false
	}
}

// evalPrefilter evaluates the scan's prefilter predicate against the
// partially-deformed o.scanRow. Only columns [0, prefilter.MaxCols) are valid;
// planScanPrefilter guarantees the predicate reads no further.
//
// A non-nil error is NOT raised here. The same predicate is about to be
// evaluated by the filterOp above on the same values, so letting the error
// surface from there keeps error position, message and ordering byte-identical
// to a build with the prefilter disabled.
func (o *seqScanOp) evalPrefilter() (bool, error) {
	// evalExprSlot with a cached SlotView, not evalExpr: evalExpr boxes the
	// Row into a SlotView interface on every call, and boxing a slice
	// heap-allocates (runtime.convTslice). That was one allocation per scanned
	// row — 32 % of the query's total — to recompute a value that never
	// changes, since o.scanRow's backing array is stable for the scan.
	if o.scanSlot == nil {
		o.scanSlot = rowSlotView(o.scanRow)
	}
	// Compiled form when Open produced one (integer kind-switch dispatch plus
	// build-time constant folding), otherwise the interpreter. evalFastExpr
	// delegates every kind it does not compile back to evalExprSlot via
	// ExprAdapter, so the two agree by construction rather than by parallel
	// maintenance.
	var (
		v   Datum
		err error
	)
	if o.pfIdx != noExpr {
		v, err = evalFastExpr(o.pfSlab, o.pfIdx, o.scanSlot, o.ctx)
	} else {
		v, err = evalExprSlot(o.prefilter.pred, o.scanSlot, o.ctx)
	}
	if err != nil {
		return true, err
	}
	// Character-for-character filterOp.Next's keep condition (operators.go).
	// It must stay that way: any divergence turns a "cannot remove a row the
	// filter would keep" guarantee into a wrong answer. NULL and non-boolean
	// both reject, which is what three-valued logic requires.
	return !v.IsNull() && v.Kind == KindBool && v.BoolValue(), nil
}
