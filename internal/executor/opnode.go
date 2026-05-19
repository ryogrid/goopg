// Package executor opnode.go — Phase C.1 concrete-type Volcano executor.
//
// This file implements the OpNode sum-type infrastructure described in
// docs/design/perf-optimize/03-executor-concrete.md §3–7. The design
// uses concrete state structs behind an `any` field (GC-safe) instead of
// the raw-byte approach sketched in the design doc, so that Phase C.1
// can land before Phase D3 (pointer-free Pin) and Phase C.3 (plan-tree
// mctx). When those phases land, the `any` field can be replaced with
// the raw-bytes layout at the same API boundary.
//
// Migration status (Phase C.1):
//   migrated (full concrete dispatch, no interface per row):
//     OpSeqScan, OpFilter, OpProject, OpLimit
//   adapter (legacy Operator interface, one type-assert per Next call):
//     all other operator kinds
//
// The split means: for query paths that only touch the four migrated
// operators (e.g. SELECT with a predicate and column list), every call
// in the opNext dispatch chain is a switch-arm call with no itab
// lookup. Unmigrated paths incur one type assertion per opNext call (the
// adapter) plus the legacy op.Next() interface cost — identical to the
// pre-Phase-C performance.
package executor

import (
	"github.com/goopg/goopg/internal/planner"
)

// ---------------------------------------------------------------------------
// Slot — concrete row-bearing struct (replaces TupleSlot interface in
// the hot path).
//
// Slot implements both TupleSlot and SlotView so it can be passed to
// any existing function that accepts those interfaces, enabling gradual
// migration without changing every call site at once.
// ---------------------------------------------------------------------------

// Slot is a concrete, stack-allocatable row vessel. Phase C callers
// preallocate one Slot per goroutine and reuse it across opNext calls.
// Cells is overwritten each call; consumers that need to retain rows
// past the next opNext call must copy via CopyInto.
//
// HasRow distinguishes two zero-Cells cases:
//   - HasRow=false, Cells=[] → DML nil-row (INSERT/UPDATE/DELETE produced
//     no result row; pass through to sink without evaluating projections).
//   - HasRow=true, Cells=[] → valid row with no columns (e.g. the
//     input row for SELECT 1, which comes from a zero-column Values node).
//
// Operators that receive a DML nil-row must propagate it as-is.
// Operators like projectOp must evaluate targets even when Cells is empty
// (the target expressions may be constants that don't reference input).
type Slot struct {
	schema planner.Schema
	Cells  []Datum
	HasRow bool // false == DML nil-row; true == real row (may have 0 cols)
}

// Reset truncates Cells to zero length, re-using the backing array.
// HasRow is set to false (no row present after reset).
func (s *Slot) Reset() {
	s.Cells = s.Cells[:0]
	s.HasRow = false
}

// CopyInto deep-copies s into dst, making dst independent of s.
// Used by sort, hash-join build, aggregate, and the Run boundary.
func (s *Slot) CopyInto(dst *Slot) {
	dst.schema = s.schema
	if cap(dst.Cells) < len(s.Cells) {
		dst.Cells = make([]Datum, len(s.Cells))
	}
	dst.Cells = dst.Cells[:len(s.Cells)]
	copy(dst.Cells, s.Cells)
}

// ----- TupleSlot / SlotView interface implementations ------------------

// Get implements SlotView.
func (s *Slot) Get(col int) Datum { return s.Cells[col] }

// IsNull implements SlotView.
func (s *Slot) IsNull(col int) bool { return s.Cells[col].Kind == KindNull }

// Schema implements TupleSlot.
func (s *Slot) Schema() planner.Schema { return s.schema }

// Width implements TupleSlot.
func (s *Slot) Width() int { return len(s.Cells) }

// Row implements TupleSlot — returns a view over Cells (not a copy).
func (s *Slot) Row() Row { return Row(s.Cells) }

// Materialize implements TupleSlot — returns an independent
// MaterializedSlot with a deep copy of Cells.
func (s *Slot) Materialize() *MaterializedSlot {
	return &MaterializedSlot{
		schema: s.schema,
		row:    cloneRowOwned(Row(s.Cells)),
	}
}

// Release implements TupleSlot — no-op for concrete slots.
func (s *Slot) Release() {}

// fillFromTupleSlot copies a TupleSlot into s. Used by the
// opAdapterState adapter for non-migrated operators.
// ts==nil sets HasRow=false (DML nil-row signal).
// ts!=nil sets HasRow=true (real row, even when Row() is empty).
func (s *Slot) fillFromTupleSlot(ts TupleSlot) {
	if ts == nil {
		s.Cells = s.Cells[:0]
		s.schema = nil
		s.HasRow = false
		return
	}
	row := ts.Row()
	if cap(s.Cells) < len(row) {
		s.Cells = make([]Datum, len(row))
	}
	s.Cells = s.Cells[:len(row)]
	copy(s.Cells, row)
	s.schema = ts.Schema()
	s.HasRow = true
}

// ---------------------------------------------------------------------------
// OpKind — discriminant for the OpNode sum-type.
// ---------------------------------------------------------------------------

// OpKind is the discriminant for OpNode.
type OpKind uint8

const (
	OpInvalid OpKind = iota

	// Migrated operators (concrete dispatch, no interface per Next call).
	OpSeqScan
	OpFilter
	OpProject
	OpLimit

	// Adapter — wraps a legacy Operator interface.
	// All unmigrated operators use this until they are individually
	// promoted to a concrete kind.
	OpAdapter
)

// ---------------------------------------------------------------------------
// OpNode — the tagged-union operator node.
// ---------------------------------------------------------------------------

// OpNode is the central node type for the Phase C execution engine.
// Each query plan corresponds to a tree of OpNodes rooted at one
// OpNode. The tree is built by BuildFast and driven by RunFast.
//
// children: childA is the primary child (left for joins, inner scan,
// etc.); childB is the secondary child (right for joins, etc.).
// nil means "no child". The slab/index encoding from the design doc
// (§3) is deferred to Phase C.2; pointer-based children are used
// here for simplicity and will be refactored once all hot-path
// operators are migrated.
type OpNode struct {
	Kind   OpKind
	childA *OpNode // primary child; nil if none
	childB *OpNode // secondary child; nil if none
	state  any     // concrete per-Kind state struct (GC-safe)
}

// ---------------------------------------------------------------------------
// Per-kind state structs for migrated operators.
// ---------------------------------------------------------------------------

// seqScanOp state is the existing *seqScanOp — Phase C.1 reuses the
// struct in-place rather than duplicating its complex heap-scan logic.
// OpSeqScan's seqScanOpNext calls seqScanOp.Next() directly (concrete
// method call, not interface dispatch, because the type-assert produces
// *seqScanOp not Operator).

// filterState is the per-node state for OpFilter.
type filterState struct {
	pred planner.Expr
	ctx  *Context
}

// projectState is the per-node state for OpProject.
type projectState struct {
	plan     *planner.Project
	schema   planner.Schema
	ctx      *Context
	srcSlot  Slot    // temp slot for the child's output
	outCells []Datum // persistent output buffer, reused across Next calls
}

// limitState is the per-node state for OpLimit.
type limitState struct {
	plan        *planner.Limit
	limitCount  int64 // -1 means unlimited
	offsetCount int64
	emitted     int64
	skipped     int64
}

// opAdapterState wraps a legacy Operator for non-migrated operators.
type opAdapterState struct {
	op Operator
}

// ---------------------------------------------------------------------------
// opOpen — recursive tree-open. Called once before the first opNext.
// ---------------------------------------------------------------------------

// opOpen opens the op-tree rooted at n. ctx is the execution context
// for the statement; all operators in the tree receive it.
func opOpen(n *OpNode, ctx *Context) error {
	if n == nil {
		return nil
	}
	switch n.Kind {
	case OpSeqScan:
		op := n.state.(*seqScanOp)
		return op.Open(ctx)

	case OpFilter:
		s := n.state.(*filterState)
		s.ctx = ctx
		return opOpen(n.childA, ctx)

	case OpProject:
		s := n.state.(*projectState)
		s.ctx = ctx
		if len(s.plan.Targets) > 0 {
			if cap(s.outCells) < len(s.plan.Targets) {
				s.outCells = make([]Datum, len(s.plan.Targets))
			}
			s.outCells = s.outCells[:len(s.plan.Targets)]
		}
		return opOpen(n.childA, ctx)

	case OpLimit:
		s := n.state.(*limitState)
		s.emitted = 0
		s.skipped = 0
		s.limitCount = -1
		s.offsetCount = 0
		if err := opOpen(n.childA, ctx); err != nil {
			return err
		}
		if s.plan.Limit != nil {
			v, err := evalExpr(s.plan.Limit, nil, ctx)
			if err != nil {
				return err
			}
			if v.Kind != KindInt {
				return &ExecError{Code: "42804", Pos: s.plan.Pos(),
					Message: "LIMIT must be integer"}
			}
			s.limitCount = v.Int
		}
		if s.plan.Offset != nil {
			v, err := evalExpr(s.plan.Offset, nil, ctx)
			if err != nil {
				return err
			}
			if v.Kind != KindInt {
				return &ExecError{Code: "42804", Pos: s.plan.Pos(),
					Message: "OFFSET must be integer"}
			}
			s.offsetCount = v.Int
		}
		return nil

	case OpAdapter:
		s := n.state.(*opAdapterState)
		return s.op.Open(ctx)

	default:
		return &ExecError{Code: "XX000",
			Message: "opOpen: unknown OpKind"}
	}
}

// ---------------------------------------------------------------------------
// opClose — recursive tree-close. Called after iteration ends.
// ---------------------------------------------------------------------------

// opClose closes the op-tree rooted at n, releasing resources.
// Returns the first error encountered but continues closing remaining
// nodes.
func opClose(n *OpNode) error {
	if n == nil {
		return nil
	}
	switch n.Kind {
	case OpSeqScan:
		op := n.state.(*seqScanOp)
		return op.Close()

	case OpFilter:
		return opClose(n.childA)

	case OpProject:
		s := n.state.(*projectState)
		s.outCells = nil
		s.srcSlot.Reset()
		return opClose(n.childA)

	case OpLimit:
		return opClose(n.childA)

	case OpAdapter:
		s := n.state.(*opAdapterState)
		return s.op.Close()

	default:
		return &ExecError{Code: "XX000",
			Message: "opClose: unknown OpKind"}
	}
}

// ---------------------------------------------------------------------------
// opNext — the single hot-path entry point.
// ---------------------------------------------------------------------------

// opNext advances the op-tree one row. On success the row data is in
// dst.Cells and dst.schema. Returns EOF when exhausted. Returns
// (nil, nil) from the legacy interface is represented as a zero-length
// dst.Cells with a nil error (callers should check len(dst.Cells)==0).
//
// dst is reused across calls; consumers that need to retain the row
// past the next opNext call must copy via dst.CopyInto.
func opNext(n *OpNode, dst *Slot) error {
	if n == nil {
		return EOF
	}
	switch n.Kind {
	case OpSeqScan:
		return seqScanOpNext(n, dst)
	case OpFilter:
		return filterOpNext(n, dst)
	case OpProject:
		return projectOpNext(n, dst)
	case OpLimit:
		return limitOpNext(n, dst)
	case OpAdapter:
		return adapterOpNext(n, dst)
	default:
		return &ExecError{Code: "XX000",
			Message: "opNext: unknown OpKind"}
	}
}

// ---------------------------------------------------------------------------
// Per-kind opNext kernels.
// ---------------------------------------------------------------------------

// seqScanOpNext drives the existing *seqScanOp.Next method. The call
// is a DIRECT method call (not interface dispatch) because the
// type assertion yields *seqScanOp, not Operator. The returned
// TupleSlot is materialised into dst.
func seqScanOpNext(n *OpNode, dst *Slot) error {
	op := n.state.(*seqScanOp) // direct concrete call below — NOT interface dispatch
	slot, err := op.Next()
	if err != nil {
		return err
	}
	dst.fillFromTupleSlot(slot) // nil slot → HasRow=false; real slot → HasRow=true
	return nil
}

// filterOpNext applies the predicate and passes matching rows from
// childA into dst. Mirrors filterOp.Next() but calls opNext on the
// child (switch dispatch) instead of child.Next() (interface dispatch).
func filterOpNext(n *OpNode, dst *Slot) error {
	s := n.state.(*filterState)
	rejected := 0
	for {
		// Cancellation check every 4096 rejected rows (M0062-followup).
		if rejected&0xFFF == 0 && s.ctx != nil && s.ctx.Ctx != nil {
			if err := s.ctx.Ctx.Err(); err != nil {
				return &ExecError{Code: "57014",
					Message: "canceling statement due to user request"}
			}
		}
		if err := opNext(n.childA, dst); err != nil {
			return err
		}
		// DML / utility ops surface nil-row (HasRow=false).
		// Pass them through without evaluating the predicate.
		if !dst.HasRow {
			return nil
		}
		v, err := evalExprSlot(s.pred, dst, s.ctx)
		if err != nil {
			return err
		}
		if !v.IsNull() && v.Kind == KindBool && v.BoolValue() {
			return nil // predicate passed; dst is already filled
		}
		rejected++
	}
}

// projectOpNext evaluates the target-list expressions against the
// child's output and writes the projected columns into dst.
// Mirrors projectOp.Next() but uses opNext for the child call.
func projectOpNext(n *OpNode, dst *Slot) error {
	s := n.state.(*projectState)
	s.srcSlot.Reset()
	if err := opNext(n.childA, &s.srcSlot); err != nil {
		return err
	}
	// DML / utility ops surface nil-row (HasRow=false).
	// Propagate without evaluating target-list expressions.
	if !s.srcSlot.HasRow {
		dst.Reset()
		return nil
	}
	inRow := Row(s.srcSlot.Cells)
	targets := s.plan.Targets
	if cap(s.outCells) < len(targets) {
		s.outCells = make([]Datum, len(targets))
	}
	s.outCells = s.outCells[:len(targets)]
	for i, t := range targets {
		v, err := evalExpr(t, inRow, s.ctx)
		if err != nil {
			return err
		}
		s.outCells[i] = v
	}
	dst.Cells = s.outCells
	dst.schema = s.schema
	dst.HasRow = true // always a real row when we reach here
	return nil
}

// limitOpNext enforces LIMIT / OFFSET by skipping and counting rows
// from childA. Mirrors limitOp.Next() but uses opNext for the child.
func limitOpNext(n *OpNode, dst *Slot) error {
	s := n.state.(*limitState)
	for s.skipped < s.offsetCount {
		if err := opNext(n.childA, dst); err != nil {
			return err
		}
		s.skipped++
	}
	if s.limitCount >= 0 && s.emitted >= s.limitCount {
		return EOF
	}
	if err := opNext(n.childA, dst); err != nil {
		return err
	}
	s.emitted++
	return nil
}

// adapterOpNext drives a legacy Operator.Next() call and materialises
// the result into dst. One type assertion per call (cheap O(1) op),
// then a direct method call on the concrete Operator implementation.
// slot==nil → dst.HasRow=false (DML nil-row); slot!=nil → dst.HasRow=true.
func adapterOpNext(n *OpNode, dst *Slot) error {
	s := n.state.(*opAdapterState)
	slot, err := s.op.Next()
	if err != nil {
		return err
	}
	dst.fillFromTupleSlot(slot) // nil slot → HasRow=false; real slot → HasRow=true
	return nil
}
