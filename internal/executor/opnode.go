// Package executor opnode.go — Phase C.2 concrete-type Volcano executor.
//
// This file implements the OpNode sum-type infrastructure described in
// docs/design/perf-optimize/03-executor-concrete.md §3–7. The design
// uses concrete state structs behind an `any` field (GC-safe) instead of
// the raw-byte approach sketched in the design doc, so that Phase C.2
// can land before Phase D3 (pointer-free Pin) and Phase C.3 (plan-tree
// mctx). When those phases land, the `any` field can be replaced with
// the raw-bytes layout at the same API boundary.
//
// Migration status (Phase C.2):
//   migrated (full concrete dispatch, no interface per row):
//     OpSeqScan, OpFilter, OpProject, OpLimit
//   adapter (legacy Operator interface, one type-assert per Next call):
//     all other operator kinds
//
// Phase C.2 changes from C.1:
//   - OpNode children are now int32 slab indices (noChild = -1) instead of
//     *OpNode pointers, eliminating GC-scanned pointers in the hot tree.
//   - opTreeSlab holds the backing []OpNode slice; all nodes are appended
//     during BuildFast and the slab is immutable after BuildFast returns.
//   - opNodeOperator and OpIterator hold *opTreeSlab + int32 index instead
//     of *OpNode.
//   - CopyInto renamed to CopyTo (Phase C.3 will add mctx parameter).
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
// past the next opNext call must copy via CopyTo.
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

// CopyTo deep-copies s into dst, making dst independent of s.
// Used by sort, hash-join build, aggregate, and the Run boundary.
func (s *Slot) CopyTo(dst *Slot) {
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

	// Phase C.1 follow-up: DML operators (no Operator child — they drive
	// storage directly via extractScan).
	OpUpdate
	OpDelete

	// Phase C.1 follow-up: Sort (uses opNodeOperator bridge for child so
	// the child subtree runs on concrete dispatch while sortOp itself is
	// unchanged).
	OpSort

	// Phase C.1 follow-up: Insert (opNodeOperator bridge for the VALUES
	// child; ON CONFLICT path still uses OpAdapter).
	OpInsert

	// Phase C.1 follow-up: Join (opNodeOperator bridges for left/right
	// children; the joinOp itself is unchanged).
	OpJoin

	// Adapter — wraps a legacy Operator interface.
	// All unmigrated operators use this until they are individually
	// promoted to a concrete kind.
	OpAdapter
)

// ---------------------------------------------------------------------------
// opTreeSlab — backing store for an OpNode tree.
// ---------------------------------------------------------------------------

// noChild is the sentinel index meaning "no child in this slot".
const noChild int32 = -1

// opTreeSlab is the backing store for an OpNode tree. All nodes are
// appended during BuildFast; the slab is immutable after BuildFast returns.
// opNodeOperator and OpIterator hold a *opTreeSlab pointer so they always
// see the final slice, even if append relocated the backing array during build.
type opTreeSlab struct {
	ops []OpNode
}

// add appends n to the slab and returns its index.
func (tree *opTreeSlab) add(n OpNode) int32 {
	idx := int32(len(tree.ops))
	tree.ops = append(tree.ops, n)
	return idx
}

// ---------------------------------------------------------------------------
// OpNode — the tagged-union operator node.
// ---------------------------------------------------------------------------

// OpNode is the central node type for the Phase C execution engine.
// Each query plan corresponds to a tree of OpNodes stored in an opTreeSlab.
// The tree is built by BuildFast (which populates the slab) and driven by
// RunFast. Children are encoded as int32 indices into the slab; noChild (-1)
// means "no child". This eliminates GC-scanned pointers in the hot tree.
type OpNode struct {
	Kind   OpKind
	childA int32 // index into ops slab; noChild (-1) if none
	childB int32 // index into ops slab; noChild (-1) if none
	state  any   // concrete per-Kind state struct (GC-safe)
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

// updateOpState wraps a concrete *updateOp (no Operator child).
type updateOpState struct {
	op *updateOp
}

// deleteOpState wraps a concrete *deleteOp (no Operator child).
type deleteOpState struct {
	op *deleteOp
}

// sortOpState wraps a *sortOp whose child is an opNodeOperator bridge,
// so the child subtree uses concrete dispatch while sortOp is unchanged.
type sortOpState struct {
	op     *sortOp
	schema planner.Schema // pre-computed from *planner.Sort.Output()
}

type insertOpState struct {
	op *insertOp
}

type joinOpState struct {
	op     *joinOp
	schema planner.Schema // pre-computed from *planner.Join.Output()
}

// ---------------------------------------------------------------------------
// opNodeOperator — bridges a slab node into the Operator interface.
//
// Used when a legacy operator (sortOp, joinOp, …) needs an Operator child
// but the child is actually an OpNode in a slab. One extra function call per
// child Next() at this boundary; the child subtree still uses concrete
// switch dispatch.
// ---------------------------------------------------------------------------

type opNodeOperator struct {
	tree   *opTreeSlab // shared slab; valid after BuildFast returns
	idx    int32
	schema planner.Schema
	dst    Slot
}

func (w *opNodeOperator) Open(ctx *Context) error { return opOpen(w.tree.ops, w.idx, ctx) }

func (w *opNodeOperator) Next() (TupleSlot, error) {
	w.dst.Reset()
	if err := opNext(w.tree.ops, w.idx, &w.dst); err != nil {
		return nil, err
	}
	if !w.dst.HasRow {
		return nil, nil // DML nil-row → propagate as nil TupleSlot
	}
	return &w.dst, nil
}

func (w *opNodeOperator) Close() error           { return opClose(w.tree.ops, w.idx) }
func (w *opNodeOperator) Schema() planner.Schema { return w.schema }

// ---------------------------------------------------------------------------
// OpIterator — wraps an opTreeSlab as an Operator for backward-compatible wiring.
//
// OpIterator implements both Operator and RowCounter so it can replace
// executor.Build in the server dispatch loop without changing the loop's
// interface. BuildFastIterator is the drop-in replacement for Build.
// ---------------------------------------------------------------------------

// OpIterator wraps an opTreeSlab as an Operator so the server dispatch
// loop can use concrete Phase C execution without structural changes.
type OpIterator struct {
	tree    *opTreeSlab
	rootIdx int32
	plan    planner.Node
	dst     Slot
}

// BuildFastIterator builds an OpNode tree via BuildFast and wraps it in an
// OpIterator. It is the drop-in replacement for executor.Build in dispatch.
func BuildFastIterator(plan planner.Node) (*OpIterator, error) {
	tree, rootIdx, err := BuildFast(plan)
	if err != nil {
		return nil, err
	}
	return &OpIterator{tree: tree, rootIdx: rootIdx, plan: plan}, nil
}

// Open implements Operator.
func (it *OpIterator) Open(ctx *Context) error { return opOpen(it.tree.ops, it.rootIdx, ctx) }

// Next implements Operator. Returns nil TupleSlot for DML nil-rows (preserving
// the legacy nil-slot convention that the dispatch loop checks with schema==nil).
func (it *OpIterator) Next() (TupleSlot, error) {
	it.dst.Reset()
	if err := opNext(it.tree.ops, it.rootIdx, &it.dst); err != nil {
		return nil, err
	}
	if !it.dst.HasRow {
		return nil, nil
	}
	return &it.dst, nil
}

// Close implements Operator.
func (it *OpIterator) Close() error { return opClose(it.tree.ops, it.rootIdx) }

// Schema implements Operator. For read plans, returns plan.Output(). For DML
// operators still in the adapter, delegates to the adapter's Schema() after
// Open (e.g. CALL with dynamic OUT-param schema). For migrated DML (OpUpdate,
// OpDelete), returns plan.Output() which carries the RETURNING schema or nil.
func (it *OpIterator) Schema() planner.Schema {
	if s := it.plan.Output(); s != nil {
		return s
	}
	if it.tree != nil && it.tree.ops[it.rootIdx].Kind == OpAdapter {
		return it.tree.ops[it.rootIdx].state.(*opAdapterState).op.Schema()
	}
	return nil
}

// RowsAffected implements RowCounter for DML operators.
func (it *OpIterator) RowsAffected() int64 {
	if it.tree == nil {
		return 0
	}
	n := &it.tree.ops[it.rootIdx]
	switch n.Kind {
	case OpUpdate:
		return n.state.(*updateOpState).op.RowsAffected()
	case OpDelete:
		return n.state.(*deleteOpState).op.RowsAffected()
	case OpInsert:
		return n.state.(*insertOpState).op.RowsAffected()
	case OpSort:
		// sortOp is a pass-through; rows-affected comes from its underlying child.
		// Sorts appear over DML in rare cases (RETURNING … ORDER BY); the child
		// RowCounter is not accessible here, so return 0 (correct for SELECT-sort).
		return 0
	case OpAdapter:
		if rc, ok := n.state.(*opAdapterState).op.(RowCounter); ok {
			return rc.RowsAffected()
		}
	}
	return 0
}

// ---------------------------------------------------------------------------
// opOpen — recursive tree-open. Called once before the first opNext.
// ---------------------------------------------------------------------------

// opOpen opens the op-tree node at ops[idx]. ctx is the execution context
// for the statement; all operators in the tree receive it.
func opOpen(ops []OpNode, idx int32, ctx *Context) error {
	if idx == noChild {
		return nil
	}
	n := &ops[idx]
	switch n.Kind {
	case OpSeqScan:
		op := n.state.(*seqScanOp)
		return op.Open(ctx)

	case OpFilter:
		s := n.state.(*filterState)
		s.ctx = ctx
		return opOpen(ops, n.childA, ctx)

	case OpProject:
		s := n.state.(*projectState)
		s.ctx = ctx
		if len(s.plan.Targets) > 0 {
			if cap(s.outCells) < len(s.plan.Targets) {
				s.outCells = make([]Datum, len(s.plan.Targets))
			}
			s.outCells = s.outCells[:len(s.plan.Targets)]
		}
		return opOpen(ops, n.childA, ctx)

	case OpLimit:
		s := n.state.(*limitState)
		s.emitted = 0
		s.skipped = 0
		s.limitCount = -1
		s.offsetCount = 0
		if err := opOpen(ops, n.childA, ctx); err != nil {
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

	case OpUpdate:
		s := n.state.(*updateOpState)
		return s.op.Open(ctx)

	case OpDelete:
		s := n.state.(*deleteOpState)
		return s.op.Open(ctx)

	case OpSort:
		s := n.state.(*sortOpState)
		return s.op.Open(ctx)

	case OpInsert:
		s := n.state.(*insertOpState)
		return s.op.Open(ctx)

	case OpJoin:
		s := n.state.(*joinOpState)
		return s.op.Open(ctx)

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

// opClose closes the op-tree node at ops[idx], releasing resources.
// Returns the first error encountered but continues closing remaining
// nodes.
func opClose(ops []OpNode, idx int32) error {
	if idx == noChild {
		return nil
	}
	n := &ops[idx]
	switch n.Kind {
	case OpSeqScan:
		op := n.state.(*seqScanOp)
		return op.Close()

	case OpFilter:
		return opClose(ops, n.childA)

	case OpProject:
		s := n.state.(*projectState)
		s.outCells = nil
		s.srcSlot.Reset()
		return opClose(ops, n.childA)

	case OpLimit:
		return opClose(ops, n.childA)

	case OpUpdate:
		s := n.state.(*updateOpState)
		return s.op.Close()

	case OpDelete:
		s := n.state.(*deleteOpState)
		return s.op.Close()

	case OpSort:
		s := n.state.(*sortOpState)
		return s.op.Close()

	case OpInsert:
		s := n.state.(*insertOpState)
		return s.op.Close()

	case OpJoin:
		s := n.state.(*joinOpState)
		return s.op.Close()

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
// past the next opNext call must copy via dst.CopyTo.
func opNext(ops []OpNode, idx int32, dst *Slot) error {
	if idx == noChild {
		return EOF
	}
	n := &ops[idx]
	switch n.Kind {
	case OpSeqScan:
		return seqScanOpNext(n, dst)
	case OpFilter:
		return filterOpNext(ops, n, dst)
	case OpProject:
		return projectOpNext(ops, n, dst)
	case OpLimit:
		return limitOpNext(ops, n, dst)
	case OpUpdate:
		return updateOpKernelNext(n, dst)
	case OpDelete:
		return deleteOpKernelNext(n, dst)
	case OpSort:
		return sortOpKernelNext(n, dst)
	case OpInsert:
		return insertOpKernelNext(n, dst)
	case OpJoin:
		return joinOpKernelNext(n, dst)
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
func filterOpNext(ops []OpNode, n *OpNode, dst *Slot) error {
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
		if err := opNext(ops, n.childA, dst); err != nil {
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
func projectOpNext(ops []OpNode, n *OpNode, dst *Slot) error {
	s := n.state.(*projectState)
	s.srcSlot.Reset()
	if err := opNext(ops, n.childA, &s.srcSlot); err != nil {
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
func limitOpNext(ops []OpNode, n *OpNode, dst *Slot) error {
	s := n.state.(*limitState)
	for s.skipped < s.offsetCount {
		if err := opNext(ops, n.childA, dst); err != nil {
			return err
		}
		s.skipped++
	}
	if s.limitCount >= 0 && s.emitted >= s.limitCount {
		return EOF
	}
	if err := opNext(ops, n.childA, dst); err != nil {
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

// updateOpKernelNext drives a concrete *updateOp directly (no interface dispatch).
// updateOp has no Operator child — it drives storage via extractScan internally.
func updateOpKernelNext(n *OpNode, dst *Slot) error {
	s := n.state.(*updateOpState)
	slot, err := s.op.Next()
	if err != nil {
		return err
	}
	dst.fillFromTupleSlot(slot)
	return nil
}

// deleteOpKernelNext drives a concrete *deleteOp directly (no interface dispatch).
func deleteOpKernelNext(n *OpNode, dst *Slot) error {
	s := n.state.(*deleteOpState)
	slot, err := s.op.Next()
	if err != nil {
		return err
	}
	dst.fillFromTupleSlot(slot)
	return nil
}

// sortOpKernelNext drives a concrete *sortOp directly. sortOp.Open() already
// collected and sorted all rows from its child (an opNodeOperator bridging the
// child OpNode, so the child ran on concrete dispatch). sortOp.Next() returns
// pre-sorted rows without calling child.Next() again.
func sortOpKernelNext(n *OpNode, dst *Slot) error {
	s := n.state.(*sortOpState)
	slot, err := s.op.Next()
	if err != nil {
		return err
	}
	dst.fillFromTupleSlot(slot)
	return nil
}

func insertOpKernelNext(n *OpNode, dst *Slot) error {
	s := n.state.(*insertOpState)
	slot, err := s.op.Next()
	if err != nil {
		return err
	}
	dst.fillFromTupleSlot(slot)
	return nil
}

func joinOpKernelNext(n *OpNode, dst *Slot) error {
	s := n.state.(*joinOpState)
	slot, err := s.op.Next()
	if err != nil {
		return err
	}
	dst.fillFromTupleSlot(slot)
	return nil
}
