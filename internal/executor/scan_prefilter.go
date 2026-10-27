package executor

import (
	"sync/atomic"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// scanPrefilterForceLate is a TEST-ONLY seam (like scanSkipDisabled): when set,
// planScanPrefilter declines every qual, so the scan evaluates it LATE on the
// finished row — the position the deleted filterOp occupied. Tests use it as
// the oracle the EARLY position must agree with (values and error position).
// It replaced E-17/EX3-08's operational switch GOOPG_SCAN_PREFILTER (default
// on, `=off` forced late), which M0147-0034 retired: no gate, CI or bench
// script ever set it. Read at Build time, so a toggle takes effect from the
// next statement rather than mid-scan.
var scanPrefilterForceLate atomic.Bool

// scanPrefilter is the EARLY half of the scan's absorbed qual (E-17 / EX3-08
// cut 2, design docs/design/executor-ex3-08-scan-resident-qual/DESIGN.md).
//
// The shape is PostgreSQL's, and now actually is: PG carries the qual on the
// scan plan node (`Scan.plan.qual`), `ExecScanExtended` evaluates it exactly
// once per tuple before projection (`execScan.h:223`), and there is no Filter
// node. goopg's `seqScanOp` absorbs the predicate of a `Filter` sitting
// directly above it and `buildNode`/`buildRec` then omit the `filterOp`
// entirely, so the predicate is evaluated ONCE.
//
// PG gets "deform only what the qual reads" for free from its lazy slot:
// ExecInitQual builds one ExprState for the whole AND-list with a single
// leading EEOP_SCAN_FETCHSOME whose last_var is the max attnum over all
// clauses (execExpr.c:2933-2938), and the projection deforms the rest under
// its own bound. goopg has no lazy slot (D-03 PackedSlot is inert), so it
// emulates that bound with a two-phase deform, and MaxCols is goopg's
// last_var.
//
// The qual is evaluated at ONE of two positions per row, never both:
//
//  1. EARLY — after cols [0, MaxCols) are deformed, before the tail deform,
//     the detoast and the deep copy. This is where the win is.
//  2. LATE — immediately before the scan yields, on the finished row. This is
//     definitionally the position the deleted filterOp occupied, so it is
//     value-for-value what filterOp saw.
//
// Every case the old design handled by "abstain and let filterOp decide
// alone" is handled by evaluating LATE. That is what preserves the old
// whitelist's failure direction: an expression kind the analysis cannot judge
// early, a post-decode row rewrite it does not model, or a toasted prefix
// costs PERFORMANCE (one late evaluation, i.e. exactly the pre-change
// behaviour) and never correctness. The wrong-answer direction — dropping a
// row the qual would have kept — is guarded by the MaxCols bound, by
// poisonDeformTail, and by PlanScanQual being fail-closed, not by a second
// evaluation.
type scanPrefilter struct {
	pred optimizer.Expr
	// MaxCols is the exclusive upper bound on the column indexes pred reads:
	// the scan deforms cols[0:MaxCols] before evaluating it.
	MaxCols int
}

// planScanPrefilter decides whether pred can be evaluated on the scan's
// deformed prefix and, if so, how many leading columns it needs. ok=false
// means "the scan must evaluate this qual LATE" — it does NOT mean "the scan
// does not evaluate it". The analysis itself lives in package optimizer,
// built on the exhaustiveness-gated walkExprRefs primitive; see
// optimizer.PlanScanQual for why it is not a hand-written type switch here.
func planScanPrefilter(pred optimizer.Expr, ncols int) (scanPrefilter, bool) {
	if scanPrefilterForceLate.Load() {
		return scanPrefilter{}, false
	}
	sq := optimizer.PlanScanQual(pred, ncols)
	if !sq.Early {
		return scanPrefilter{}, false
	}
	return scanPrefilter{pred: pred, MaxCols: sq.MaxCols}, true
}

// evalQual evaluates the scan's absorbed qual against slot. It is THE
// evaluator — one function, called from one of two positions in Next (early
// on the deformed prefix, late on the finished row), never both for the same
// row.
//
// A non-nil error at the EARLY position is not raised there: Next promotes
// that row to the late position and the error surfaces from exactly where
// filterOp raised it. See seqScanOp.Next and DESIGN.md §4.4 — this preserves
// goopg's current error ORDERING, in which a later per-row stage may drop the
// row first (a failed DetoastRowBound is a `continue`, not a raise) or raise
// first (the GiST SSI conflict-out). PG's order is the inverse — its qual is
// the first thing to touch the tuple — and converging on it is a separate,
// observable change (ledger: e17-qual-first-error-ordering).
func (o *seqScanOp) evalQual(slot SlotView) (bool, error) {
	var (
		v   Datum
		err error
	)
	// Compiled form when Open produced one (integer kind-switch dispatch plus
	// build-time constant folding), otherwise the interpreter. evalFastExpr
	// delegates every kind it does not compile back to evalExprSlot via
	// ExprAdapter, so the two agree by construction rather than by parallel
	// maintenance.
	if o.pfIdx != noExpr {
		v, err = evalFastExpr(o.pfSlab, o.pfIdx, slot, o.ctx)
	} else {
		v, err = evalExprSlot(o.qual, slot, o.ctx)
	}
	if err != nil {
		return false, err
	}
	// Character-for-character the deleted filterOp.Next's keep condition
	// (operators.go). The NULL arm is three-valued logic and is PG's
	// (EEOP_QUAL short-circuits the first NULL to false, execExpr.c:252-277).
	// The non-boolean arm is NOT PG — parse analysis coerces WHERE to
	// boolean, so it cannot occur — but it is preserved here byte-for-byte
	// because this slice must not change which rows survive; making it raise
	// is filed separately.
	return !v.IsNull() && v.Kind == KindBool && v.BoolValue(), nil
}

// evalPrefilterEarly evaluates the absorbed qual against the
// partially-deformed o.scanRow. Only columns [0, prefilter.MaxCols) are
// valid; PlanScanQual guarantees the qual reads no further.
func (o *seqScanOp) evalPrefilter() (bool, error) {
	// evalExprSlot with a cached SlotView, not evalExpr: evalExpr boxes the
	// Row into a SlotView interface on every call, and boxing a slice
	// heap-allocates (runtime.convTslice). That was one allocation per scanned
	// row — 32 % of the query's total — to recompute a value that never
	// changes, since o.scanRow's backing array is stable for the scan.
	if o.scanSlot == nil {
		o.scanSlot = rowSlotView(o.scanRow)
	}
	return o.evalQual(o.scanSlot)
}

// scanQualLead is the absorbed qual's leading conjunct, judged on its own
// columns before the prefilter's prefix is decoded (M0122-0012 slice 86).
// PG decodes up to the qual's last attribute once (EEOP_SCAN_FETCHSOME) and
// then runs the AND-list step by step, so a tuple its first clause rejects
// costs PG a deform it never reads. goopg's deform of a numeric or varlena
// column costs more than PG's, so the scan decodes only the lead's columns,
// evaluates the lead, and stops there when it is FALSE: that is the verdict
// the whole qual returns, because the compiled AND evaluates its left operand
// first and returns a FALSE one as the result (evalFastExpr's OpAnd
// short-circuit). The lead is a left operand on the qual's AND spine, so it is
// what the full evaluation computes first. Every other outcome — TRUE, NULL,
// an error, a decode error, a toasted cell — decides nothing, and the row
// takes the prefilter path unchanged, which evaluates the qual whole.
// TPC-DS Q28's six store_sales quals lead with `ss_quantity >= x AND
// ss_quantity <= y`: one int4 column against four, three of them numerics up
// to column 20, and about 95 % of rows fail it.
type scanQualLead struct {
	// idx is the lead's node in the prefilter's slab; noExpr means no lead
	// stage (none was found, or the probe below switched it off).
	idx int32
	// max is the lead's MaxCols, and info is colInfo with every column in
	// [0, max) the lead does not read marked skip (nil: colInfo itself).
	max  int
	info []colTypeInfo
	// seen / rejected count the lead's evaluations and FALSE verdicts. A lead
	// that rejects under a quarter of the first scanLeadProbeRows rows only
	// adds work (the rows it passes decode its columns twice), so the stage
	// is switched off for the rest of the scan.
	seen, rejected int
}

const scanLeadProbeRows = 4096

// planScanQualLead finds the lead stage for the prefilter qual compiled at
// slab[idx]. It walks the qual's AND spine and the slab together — the AND
// node's left child is its childA — and takes the first left operand that
// reads fewer columns than the whole qual. No lead (idx noExpr) when the qual
// is not an AND, a node was folded or adapted, or no left operand reads
// fewer columns.
func planScanQualLead(slab exprTreeSlab, idx int32, qual optimizer.Expr, info []colTypeInfo) scanQualLead {
	none := scanQualLead{idx: noExpr}
	full := deformScanReadSet(len(info), qual)
	if full == nil {
		return none
	}
	nFull := countRead(full)
	e := qual
	for idx != noExpr && int(idx) < len(slab) {
		b, ok := e.(*optimizer.BinaryOp)
		n := &slab[idx]
		if !ok || b.Op != parser.OpAnd || n.Kind != ExprBinaryOp || parser.OpCode(n.payload[0]) != parser.OpAnd || n.constVal != nil {
			return none
		}
		e, idx = b.Left, n.childA
		read := deformScanReadSet(len(info), e)
		if read == nil || idx == noExpr {
			return none
		}
		if countRead(read) < nFull {
			sq := optimizer.PlanScanQual(e, len(info))
			if !sq.Early || sq.MaxCols > len(info) {
				return none
			}
			return scanQualLead{idx: idx, max: sq.MaxCols, info: prefilterSkipInfo(info, sq.MaxCols, e)}
		}
	}
	return none
}

func countRead(read []bool) int {
	n := 0
	for _, r := range read {
		if r {
			n++
		}
	}
	return n
}

// leadRejects decodes the lead's columns of the tuple into o.scanRow and
// reports whether the lead is FALSE for it — then, and only then, the whole
// qual is FALSE and the row is rejected. The cells it decodes are handed to
// nothing but the lead; the prefilter path re-decodes the prefix from column
// 0 for every row it does not reject.
func (o *seqScanOp) leadRejects(data, bitmap []byte, storedNatts int) bool {
	l := &o.lead
	info := l.info
	if info == nil {
		info = o.colInfo
	}
	if _, err := o.decodeScanRowRangeInfo(info, data, bitmap, storedNatts, 0, l.max, 0); err != nil {
		return false
	}
	if needsDetoastPrefix(o.scanRow, l.max) {
		return false
	}
	if o.scanSlot == nil {
		o.scanSlot = rowSlotView(o.scanRow)
	}
	v, err := evalFastExpr(o.pfSlab, l.idx, o.scanSlot, o.ctx)
	l.seen++
	reject := err == nil && v.Kind == KindBool && !v.BoolValue()
	if reject {
		l.rejected++
	}
	if l.seen == scanLeadProbeRows && l.rejected*4 < l.seen {
		l.idx = noExpr
	}
	return reject
}
