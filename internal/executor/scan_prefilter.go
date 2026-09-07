package executor

import (
	"os"
	"sync/atomic"

	"github.com/goopg/goopg/internal/optimizer"
)

// scanPrefilterOn is E-17/EX3-08's operational kill switch. Default ON;
// GOOPG_SCAN_PREFILTER=off at server start (or SetScanPrefilterEnabled(false)
// from tests/benchmarks) restores the pre-take5 seam in which the scan
// deforms every column and filterOp is the only evaluator.
//
// It exists so the E-17 measurement arm can price the prefilter against its
// own absence on the SAME binary — the F-02 precedent (GOOPG_JOIN_SLOT_CHAIN),
// where keeping the pre-fix seam runnable is what made the delta reproducible.
// Read in planScanPrefilter, i.e. at Build time, so a toggle takes effect from
// the next statement rather than mid-scan.
var scanPrefilterOn atomic.Bool

func init() {
	scanPrefilterOn.Store(os.Getenv("GOOPG_SCAN_PREFILTER") != "off")
}

// SetScanPrefilterEnabled toggles the in-scan prefilter. Test-only API; the
// operational switch is the environment variable read at init.
func SetScanPrefilterEnabled(on bool) { scanPrefilterOn.Store(on) }

func scanPrefilterEnabled() bool { return scanPrefilterOn.Load() }

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
	if !scanPrefilterEnabled() {
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
