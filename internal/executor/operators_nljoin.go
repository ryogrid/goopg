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
	inner *indexScanOp
	ctx   *Context

	// outerWidth / innerWidth are captured in Open() from the child
	// schemas. They are constant for the operator's lifetime.
	outerWidth int
	innerWidth int

	// nullInner is a reusable nullRow(innerWidth) used both for
	// LEFT-join filler and for binding before the IndexScan
	// resolves its key (so the outer-column slot is the only
	// non-NULL value visible).
	nullInner Row
	// joinBuf is the per-Next() reusable concatenation buffer.
	joinBuf Row

	// state: when an outer row is being processed, currentOuter
	// holds it and innerExhausted tracks whether we've already
	// drained the inner's matches for this outer row.
	currentOuter   Row
	innerExhausted bool
	leftJoinEmitted bool // for LEFT outer: did we emit the null-padded fallback?
	openOnce       bool

	// M0059-0003: borrow contract. When set to BorrowedRow, Next()
	// returns o.joinBuf directly without the defensive cloneRow
	// — the parent has promised to consume the row before pulling
	// the next one. Default OwnedRow.
	borrow BorrowSemantics
}

func newNestedLoopIndexJoinOp(p *planner.NestedLoopIndexJoin, outer Operator, inner *indexScanOp) *nestedLoopIndexJoinOp {
	return &nestedLoopIndexJoinOp{plan: p, outer: outer, inner: inner}
}

// SetBorrow flips nestedLoopIndexJoinOp into borrow-on-output mode.
// (M0059-0003.)
func (o *nestedLoopIndexJoinOp) SetBorrow(s BorrowSemantics) { o.borrow = s }

func (o *nestedLoopIndexJoinOp) Schema() planner.Schema {
	return o.plan.Output()
}

func (o *nestedLoopIndexJoinOp) Open(ctx *Context) error {
	o.ctx = ctx
	if err := o.outer.Open(ctx); err != nil {
		return err
	}
	o.outerWidth = len(o.outer.Schema())
	o.innerWidth = len(o.inner.Schema())
	o.nullInner = nullRow(o.innerWidth)
	o.joinBuf = acquireRow(o.outerWidth + o.innerWidth)
	o.currentOuter = nil
	o.innerExhausted = true
	o.leftJoinEmitted = true
	// Open the inner once (acquires the relation lock and opens
	// the btree). Per-outer-row work happens in Rescan + the
	// inner's own Next() loop.
	if err := o.inner.openPrep(ctx); err != nil {
		return err
	}
	o.openOnce = true
	return nil
}

func (o *nestedLoopIndexJoinOp) Next() (Row, error) {
	for {
		// If we're still serving inner matches for the current
		// outer row, emit them first.
		if o.currentOuter != nil && !o.innerExhausted {
			innerRow, err := o.inner.Next()
			if err == EOF {
				o.innerExhausted = true
				// LEFT-join fallback: when no inner row matched
				// AND we are LEFT-join, emit the null-padded outer
				// row exactly once before advancing.
				if !o.leftJoinEmitted && o.plan.Type == planner.JoinTypeLeft {
					o.leftJoinEmitted = true
					o.fillJoinBuf(o.currentOuter, o.nullInner)
					if ok, perr := o.evalPredicate(o.joinBuf); perr != nil {
						return nil, perr
					} else if ok {
						if o.borrow == BorrowedRow {
							return o.joinBuf, nil
						}
						return cloneRow(o.joinBuf), nil
					}
				}
				// M0063-0004: Anti-join fallback. When no inner
				// match passed evalPredicate AND the join is
				// JoinTypeAnti, emit the outer row alone (the
				// "matched at least one" indicator was set when
				// any inner pass would have happened — by the
				// !o.leftJoinEmitted reuse on Anti below).
				if o.plan.Type == planner.JoinTypeAnti && !o.leftJoinEmitted {
					o.leftJoinEmitted = true
					if o.borrow == BorrowedRow {
						return o.currentOuter, nil
					}
					return cloneRow(o.currentOuter), nil
				}
				continue
			}
			if err != nil {
				return nil, err
			}
			// Mark that some inner row was produced (used by
			// LEFT and Anti's "no-match" fallbacks).
			o.leftJoinEmitted = true
			o.fillJoinBuf(o.currentOuter, innerRow)
			ok, perr := o.evalPredicate(o.joinBuf)
			if perr != nil {
				return nil, perr
			}
			if !ok {
				// Inner row failed the residual Predicate.
				// Reset the leftJoinEmitted bit so Anti's
				// "no qualifying match" fallback can fire if
				// every inner row fails.
				if o.plan.Type == planner.JoinTypeAnti {
					o.leftJoinEmitted = false
				}
				continue
			}
			// M0063-0004: Semi emits the OUTER row exactly
			// once on first qualifying match; advance to the
			// next outer.
			if o.plan.Type == planner.JoinTypeSemi {
				o.innerExhausted = true
				if o.borrow == BorrowedRow {
					return o.currentOuter, nil
				}
				return cloneRow(o.currentOuter), nil
			}
			// M0063-0004: Anti's qualifying inner match means
			// the outer row will NOT be emitted. Fast-forward
			// past remaining inner rows.
			if o.plan.Type == planner.JoinTypeAnti {
				o.innerExhausted = true
				continue
			}
			if o.borrow == BorrowedRow {
				return o.joinBuf, nil
			}
			return cloneRow(o.joinBuf), nil
		}

		// Pull the next outer row.
		outerRow, err := o.outer.Next()
		if err == EOF {
			return nil, EOF
		}
		if err != nil {
			return nil, err
		}
		o.currentOuter = outerRow

		// Bind the outer row into the joinBuf shape so the inner's
		// key expressions can resolve outer column references via
		// `o.outerRow` in `lookupKey` / `lookupRangeBounds`.
		o.fillJoinBuf(outerRow, o.nullInner)
		o.inner.BindOuter(o.joinBuf)

		if err := o.inner.Rescan(o.joinBuf); err != nil {
			return nil, err
		}
		o.innerExhausted = false
		o.leftJoinEmitted = false
	}
}

func (o *nestedLoopIndexJoinOp) Close() error {
	if o.openOnce {
		_ = o.inner.Close()
		o.openOnce = false
	}
	if o.joinBuf != nil {
		releaseRow(o.joinBuf)
		o.joinBuf = nil
	}
	return o.outer.Close()
}

// fillJoinBuf copies outer + inner into the per-Next reusable
// joinBuf. Caller must clone before retaining beyond the next
// Next call.
func (o *nestedLoopIndexJoinOp) fillJoinBuf(outer, inner Row) {
	if len(o.joinBuf) != o.outerWidth+o.innerWidth {
		o.joinBuf = acquireRow(o.outerWidth + o.innerWidth)
	}
	copy(o.joinBuf[:o.outerWidth], outer)
	copy(o.joinBuf[o.outerWidth:], inner)
}

// evalPredicate evaluates plan.Predicate against the joined row,
// or returns true when no residual predicate is present.
func (o *nestedLoopIndexJoinOp) evalPredicate(row Row) (bool, error) {
	if o.plan.Predicate == nil {
		return true, nil
	}
	v, err := evalExpr(o.plan.Predicate, row, o.ctx)
	if err != nil {
		return false, err
	}
	if v.IsNull() {
		return false, nil
	}
	return v.BoolValue(), nil
}
