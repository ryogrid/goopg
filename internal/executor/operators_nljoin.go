package executor

// Nested-Loop Index Join operator (M0054-0006b).
//
// nestedLoopIndexJoinOp drives a `*planner.IndexScan` inner side
// once per outer row. The inner's `Key` / `LowKey` / `HighKey`
// expressions are allowed to reference outer-row columns through
// the IndexScan's `BindOuter` API: before each Rescan, the operator
// constructs a "joined-shape" row of `outer ++ nullRow(innerWidth)`
// and binds it. The inner's `lookupKey` / `lookupRangeBounds` then
// resolve outer column references against that bound row.
//
// Supported join types: INNER, LEFT. For LEFT, when the inner
// probe yields zero matching rows, the operator emits a single
// `outer ++ nullRow(innerWidth)` to preserve the outer row.
//
// `plan.Predicate` carries any residual conjuncts that the
// IndexScan probe alone does not enforce (e.g. range qualifiers
// on non-indexed columns, or a non-equi conjunct ANDed onto the
// join predicate). It is evaluated per emitted row against the
// concatenated `outer ++ inner` row.
//
// The output schema is `outer.Schema() ++ inner.Schema()`. The
// inner schema is `o.plan.Inner.Output()` — the IndexScan's
// table columns.
//
// Buffer reuse follows the M0054-0005b pattern: `joinBuf` is
// reused across `Next()` calls. Callers that need to retain a
// row beyond the next `Next()` (sortOp, aggregateOp build) clone
// it via the existing `cloneRow` helper. Operators that pass the
// row through (filterOp, projectOp) re-evaluate against the
// borrowed slice safely.

import (
	"github.com/goopg/goopg/internal/planner"
)

type nestedLoopIndexJoinOp struct {
	plan  *planner.NestedLoopIndexJoin
	outer Operator
	inner nliInner
	ctx   *Context

	// joinFilterRemoved is the stats.joinFilterRejected pointer handed by
	// maybeInstrument; nil when EXPLAIN ANALYZE is not active. Incremented
	// each time evalPredicateSlot returns false (residual reject).
	joinFilterRemoved *int64

	// outerWidth / innerWidth are captured in Open() from the child
	// schemas. They are constant for the operator's lifetime.
	outerWidth int
	innerWidth int

	// nullInner is a reusable nullRow(innerWidth) used both for
	// LEFT-join filler and for binding before the IndexScan
	// resolves its key (so the outer-column slot is the only
	// non-NULL value visible).
	nullInner Row

	// M0071-0013 Stage D-1: VirtualSlot composition replaces the
	// per-Next joinBuf concat. outerMS / innerMS are persistent
	// MaterializedSlots whose .row field is overwritten in place
	// per match; virtualOut sources [outerMS, innerMS] and serves
	// as the operator's emit slot. Predicate evaluation reads via
	// virtualOut.Get(col) — zero allocation per match.
	outerMS    *MaterializedSlot
	innerMS    *MaterializedSlot
	virtualOut *VirtualSlot

	// outerOnly is the operator's emit slot for Semi / Anti, which
	// emit the outer row alone (their plan.Output() schema is the
	// outer schema). Reused; .row points to currentOuter.
	outerOnly *MaterializedSlot

	// (M0072-0001 deleted the per-outer boundRow; the IndexScan
	// now consumes o.outerMS directly via its slot-aware
	// BindOuter / Rescan signature.)

	// state: when an outer row is being processed, currentOuter
	// holds it and innerExhausted tracks whether we've already
	// drained the inner's matches for this outer row.
	currentOuter   Row
	innerExhausted bool
	// outerMatched records whether the current outer row has a
	// QUALIFYING inner match — one that passed plan.Predicate, not
	// merely one the index probe produced. LEFT's null-pad fallback and
	// Anti's emit-the-outer fallback both key off it, and both are only
	// correct under that definition: the NLI Predicate is the JOIN ON
	// residual, so an inner row failing it is not a match at all
	// (R3-1; before it, this was set on row-produced and named
	// leftJoinEmitted, which silently dropped preserved LEFT rows).
	outerMatched bool
	openOnce     bool
}

// nliInner is the protocol the NLI driver requires of its inner side:
// prepared once (openPrep), then per outer row BindOuter + Rescan, with
// Next draining the probe's matches. Satisfied by *indexScanOp (the
// bare probe) and *memoizeOp (S7's parameterized result cache, which
// forwards to a child *indexScanOp on cache misses).
type nliInner interface {
	Schema() planner.Schema
	openPrep(ctx *Context) error
	Next() (TupleSlot, error)
	BindOuter(slot SlotView, outerWidth int)
	Rescan(outerSlot SlotView, outerWidth int) error
	Close() error
}

// nliInnerIndexScan unwraps an nliInner to its underlying index scan
// (identity for a bare probe, the child for a memoize wrapper). Used by
// the FOR UPDATE TID-provider walks, which need the concrete scan.
func nliInnerIndexScan(in nliInner) *indexScanOp {
	switch x := in.(type) {
	case *indexScanOp:
		return x
	case *memoizeOp:
		return x.child
	}
	return nil
}

func newNestedLoopIndexJoinOp(p *planner.NestedLoopIndexJoin, outer Operator, inner nliInner) *nestedLoopIndexJoinOp {
	return &nestedLoopIndexJoinOp{plan: p, outer: outer, inner: inner}
}

func (o *nestedLoopIndexJoinOp) Schema() planner.Schema {
	return o.plan.Output()
}

func (o *nestedLoopIndexJoinOp) setJoinFilterRemoveCounter(p *int64) { o.joinFilterRemoved = p }

func (o *nestedLoopIndexJoinOp) Open(ctx *Context) error {
	o.ctx = ctx
	if err := o.outer.Open(ctx); err != nil {
		return err
	}
	o.outerWidth = len(o.outer.Schema())
	o.innerWidth = len(o.inner.Schema())
	o.nullInner = nullRow(o.innerWidth)

	// Build virtualOut once. Sources are [outerMS, innerMS] and
	// the column mapping is [(0,0)..(0,outerW-1), (1,0)..(1,innerW-1)].
	o.outerMS = SlotFromRow(o.outer.Schema(), nil)
	o.innerMS = SlotFromRow(o.inner.Schema(), nil)
	cols := make([]virtualCol, 0, o.outerWidth+o.innerWidth)
	for i := 0; i < o.outerWidth; i++ {
		cols = append(cols, virtualCol{sourceIdx: 0, sourceCol: int16(i)})
	}
	for i := 0; i < o.innerWidth; i++ {
		cols = append(cols, virtualCol{sourceIdx: 1, sourceCol: int16(i)})
	}
	o.virtualOut = NewVirtualSlot(o.Schema(), []TupleSlot{o.outerMS, o.innerMS}, cols)
	o.outerOnly = SlotFromRow(o.Schema(), nil)

	o.currentOuter = nil
	o.innerExhausted = true
	o.outerMatched = true
	// Open the inner once (acquires the relation lock and opens
	// the btree). Per-outer-row work happens in Rescan + the
	// inner's own Next() loop.
	if err := o.inner.openPrep(ctx); err != nil {
		return err
	}
	o.openOnce = true
	return nil
}

func (o *nestedLoopIndexJoinOp) Next() (TupleSlot, error) {
	for {
		// If we're still serving inner matches for the current
		// outer row, emit them first.
		if o.currentOuter != nil && !o.innerExhausted {
			innerSlot, err := o.inner.Next()
			if err == EOF {
				o.innerExhausted = true
				// LEFT-join fallback: when no inner row QUALIFIED
				// (no probe candidate, or none passed the residual)
				// emit the null-padded outer row exactly once.
				//
				// R3-1: the emission is unconditional. plan.Predicate
				// is the JOIN ON residual, and PG evaluates a join
				// condition only against real inner rows — never
				// against the null padding it just synthesised. This
				// code used to gate the fallback on evaluating the
				// residual against o.nullInner, where an inner-column
				// reference yields NULL -> false, dropping the
				// preserved outer row. The hash join has always done
				// it this way (openLazyHashJoin's null-pad path).
				if !o.outerMatched && o.plan.Type == planner.JoinTypeLeft {
					o.outerMatched = true
					o.outerMS.row = o.currentOuter
					o.innerMS.row = o.nullInner
					return o.virtualOut, nil
				}
				// M0063-0004: Anti-join fallback. When no inner row
				// passed the predicate AND the join is JoinTypeAnti,
				// emit the outer row alone.
				if o.plan.Type == planner.JoinTypeAnti && !o.outerMatched {
					o.outerMatched = true
					o.outerOnly.row = o.currentOuter
					return o.outerOnly, nil
				}
				continue
			}
			if err != nil {
				return nil, err
			}
			innerRow := slotRow(innerSlot)
			o.outerMS.row = o.currentOuter
			o.innerMS.row = innerRow
			ok, perr := o.evalPredicateSlot()
			if perr != nil {
				return nil, perr
			}
			if !ok {
				// The probe produced this row but the residual
				// rejected it, so it is not a match at all —
				// leave outerMatched alone and try the next
				// candidate.
				if o.joinFilterRemoved != nil {
					*o.joinFilterRemoved++
				}
				continue
			}
			// R3-1: a QUALIFYING match. Recording it here rather
			// than on row-produced is what makes both fallbacks
			// correct — LEFT null-pads iff no candidate ever
			// passed, Anti emits iff none passed. The old code set
			// the bit above and reset it on failure for Anti only,
			// a discipline that cannot work for LEFT: its loop
			// revisits candidates after emitting one, so a reset
			// would re-arm the fallback and duplicate the outer
			// row. Setting on pass needs no reset in either case.
			o.outerMatched = true
			// M0063-0004: Semi emits the OUTER row exactly
			// once on first qualifying match; advance to the
			// next outer.
			if o.plan.Type == planner.JoinTypeSemi {
				o.innerExhausted = true
				o.outerOnly.row = o.currentOuter
				return o.outerOnly, nil
			}
			// M0063-0004: Anti's qualifying inner match means
			// the outer row will NOT be emitted. Fast-forward
			// past remaining inner rows.
			if o.plan.Type == planner.JoinTypeAnti {
				o.innerExhausted = true
				continue
			}
			return o.virtualOut, nil
		}

		// Pull the next outer row.
		// Check for statement-timeout cancellation once per outer row.
		// M0097-0059: statement_timeout enforcement.
		if o.ctx != nil && o.ctx.Ctx != nil {
			if cerr := o.ctx.Ctx.Err(); cerr != nil {
				return nil, &ExecError{Code: "57014", Message: "canceling statement due to statement timeout"}
			}
		}
		outerSlot, err := o.outer.Next()
		if err == EOF {
			return nil, EOF
		}
		if err != nil {
			return nil, err
		}
		outerRow := slotRow(outerSlot)
		// M0092 (prerequisite for M0092-0002): the upstream child
		// may return a slot that ALIASES its internal buffer (e.g.,
		// projectOp.o.out after the cloneRow removal). The next
		// outer Next() would overwrite that buffer mid-inner-loop,
		// corrupting o.currentOuter. Defensively copy outerRow into
		// our own buffer (reusing capacity where possible). This is
		// strictly safer than aliasing and is unconditionally
		// correct regardless of upstream behaviour.
		if cap(o.currentOuter) < len(outerRow) {
			o.currentOuter = make(Row, len(outerRow))
		} else {
			o.currentOuter = o.currentOuter[:len(outerRow)]
		}
		copy(o.currentOuter, outerRow)

		// M0072-0001: bind the outer row into o.outerMS once and
		// pass the persistent slot directly to the inner IndexScan.
		// The inner reads outer columns via o.outerSlot.Get(col)
		// (evalExprSlot at lookupKey / lookupRangeBounds), so no
		// concatenated `boundRow` alloc is needed per outer.
		o.outerMS.row = o.currentOuter
		o.inner.BindOuter(o.outerMS, o.outerWidth)

		if err := o.inner.Rescan(o.outerMS, o.outerWidth); err != nil {
			return nil, err
		}
		o.innerExhausted = false
		o.outerMatched = false
	}
}

func (o *nestedLoopIndexJoinOp) Close() error {
	if o.openOnce {
		_ = o.inner.Close()
		o.openOnce = false
	}
	return o.outer.Close()
}

// evalPredicateSlot evaluates plan.Predicate against o.virtualOut
// (which sources [outerMS, innerMS]). Both source slots' .row
// fields must be set by the caller before invocation.
func (o *nestedLoopIndexJoinOp) evalPredicateSlot() (bool, error) {
	if o.plan.Predicate == nil {
		return true, nil
	}
	v, err := evalExprSlot(o.plan.Predicate, o.virtualOut, o.ctx)
	if err != nil {
		return false, err
	}
	if v.IsNull() {
		return false, nil
	}
	return v.BoolValue(), nil
}
