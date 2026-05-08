package executor

import (
	"container/heap"
	"io"
	"os"
	"sort"

	"github.com/goopg/goopg/internal/planner"
)

// valuesOp emits a fixed sequence of rows produced from literal
// expressions. SELECT 1 plans into a Project over a one-row Values
// with an empty input row.
type valuesOp struct {
	rows    [][]planner.Expr
	idx     int
	ctx     *Context
	schema  planner.Schema
	outSlot MaterializedSlot // M0069-0001 Stage B per-op slot reuse
}

func newValuesOp(plan *planner.Values) *valuesOp {
	return &valuesOp{rows: plan.Rows, schema: plan.Output()}
}

func (o *valuesOp) Open(ctx *Context) error { o.ctx = ctx; o.idx = 0; return nil }
func (o *valuesOp) Schema() planner.Schema  { return o.schema }
func (o *valuesOp) Close() error            { return nil }

func (o *valuesOp) Next() (TupleSlot, error) {
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	exprs := o.rows[o.idx]
	o.idx++
	row := make(Row, len(exprs))
	for i, e := range exprs {
		v, err := evalExpr(e, nil, o.ctx)
		if err != nil {
			return nil, err
		}
		row[i] = v
	}
	return o.outSlot.set(row), nil
}

// projectOp evaluates the target list against each child row.
type projectOp struct {
	child   Operator
	targets []planner.Expr
	schema  planner.Schema
	ctx     *Context
	// M0054-0005a-followup: borrow-semantics output buffer reuse.
	// M0069-0001 Stage B: outSlot wraps `out` and is the per-call
	// emit slot. Caller must consume / Materialize before next
	// Next(). The borrow flag is now structural (always
	// borrow-style at the slot level) but kept here for
	// compatibility with the legacy Borrowable interface during
	// the migration.
	borrow  BorrowSemantics
	out     Row
	outSlot MaterializedSlot
}

func newProjectOp(plan *planner.Project, child Operator) *projectOp {
	return &projectOp{child: child, targets: plan.Targets, schema: plan.Output()}
}

func (o *projectOp) Open(ctx *Context) error {
	o.ctx = ctx
	if cap(o.out) < len(o.targets) {
		o.out = acquireRow(len(o.targets))
	} else {
		o.out = o.out[:len(o.targets)]
	}
	return o.child.Open(ctx)
}
func (o *projectOp) Schema() planner.Schema { return o.schema }
func (o *projectOp) Close() error {
	releaseRow(o.out)
	o.out = nil
	return o.child.Close()
}

// SetBorrow marks projectOp as eligible to return borrowed
// rows. (M0054-0005a-followup.)
func (o *projectOp) SetBorrow(s BorrowSemantics) { o.borrow = s }

func (o *projectOp) Next() (TupleSlot, error) {
	in, err := NextRow(o.child)
	if err != nil {
		return nil, err
	}
	for i, t := range o.targets {
		v, err := evalExpr(t, in, o.ctx)
		if err != nil {
			return nil, err
		}
		o.out[i] = v
	}
	// M0069-0001 Stage B: slot points to o.out (reused buffer).
	// The legacy borrow flag is now structurally borrow-style —
	// the caller must Materialize() before retention. The
	// per-call cloneRow that the OwnedRow path used to do has
	// shifted to the consumer side (sortOp / hash-build).
	return o.outSlot.set(o.out), nil
}

// filterOp drops rows where the predicate doesn't evaluate to TRUE.
// NULL predicates exclude the row, matching SQL semantics.
type filterOp struct {
	child   Operator
	pred    planner.Expr
	ctx     *Context
	borrow  BorrowSemantics
	outSlot MaterializedSlot // M0069-0001 Stage B
}

func newFilterOp(plan *planner.Filter, child Operator) *filterOp {
	return &filterOp{child: child, pred: plan.Predicate}
}

func (o *filterOp) Open(ctx *Context) error { o.ctx = ctx; return o.child.Open(ctx) }
func (o *filterOp) Schema() planner.Schema  { return o.child.Schema() }
func (o *filterOp) Close() error            { return o.child.Close() }

// SetBorrow propagates the borrow contract to the child. filterOp
// itself never copies — it just hands through. So borrow-OK at
// filter ⇒ borrow-OK at child. (M0054-0005a-followup.)
func (o *filterOp) SetBorrow(s BorrowSemantics) {
	o.borrow = s
	if b, ok := o.child.(Borrowable); ok {
		b.SetBorrow(s)
	}
}

func (o *filterOp) Next() (TupleSlot, error) {
	rejected := 0
	for {
		// M0062-followup: a highly-selective filter can drain millions
		// of child rows without yielding to the parent, blocking
		// cancel propagation. Check ctx every 4096 rejections.
		if rejected&0xFFF == 0 && o.ctx != nil && o.ctx.Ctx != nil {
			if err := o.ctx.Ctx.Err(); err != nil {
				return nil, &ExecError{Code: "57014", Message: "canceling statement due to user request"}
			}
		}
		// M0069-0001 Stage C: pass-through. Filter returns the
		// child's slot directly when the predicate matches —
		// avoids the Row materialisation + outSlot wrap that
		// Stage B's NextRow boundary imposed.
		slot, err := o.child.Next()
		if err != nil {
			return nil, err
		}
		v, err := evalExpr(o.pred, slot.Row(), o.ctx)
		if err != nil {
			return nil, err
		}
		if !v.IsNull() && v.Kind == KindBool && v.BoolValue() {
			return slot, nil
		}
		rejected++
	}
}

// limitOp implements LIMIT/OFFSET. Both are evaluated once at Open
// so a long stream doesn't re-evaluate.
type limitOp struct {
	child       Operator
	limitExpr   planner.Expr
	offsetExpr  planner.Expr
	limitCount  int64 // -1 for no limit
	offsetCount int64
	emitted     int64
	skipped     int64
	borrow      BorrowSemantics
	outSlot     MaterializedSlot // M0069-0001 Stage B
}

// SetBorrow propagates to the child. (M0054-0005a-followup.)
func (o *limitOp) SetBorrow(s BorrowSemantics) {
	o.borrow = s
	if b, ok := o.child.(Borrowable); ok {
		b.SetBorrow(s)
	}
}

func newLimitOp(plan *planner.Limit, child Operator) *limitOp {
	return &limitOp{child: child, limitExpr: plan.Limit, offsetExpr: plan.Offset, limitCount: -1}
}

func (o *limitOp) Open(ctx *Context) error {
	if err := o.child.Open(ctx); err != nil {
		return err
	}
	if o.limitExpr != nil {
		v, err := evalExpr(o.limitExpr, nil, ctx)
		if err != nil {
			return err
		}
		if v.Kind != KindInt {
			return &ExecError{Code: "42804", Pos: o.limitExpr.Pos(), Message: "LIMIT must be integer"}
		}
		o.limitCount = v.Int
	}
	if o.offsetExpr != nil {
		v, err := evalExpr(o.offsetExpr, nil, ctx)
		if err != nil {
			return err
		}
		if v.Kind != KindInt {
			return &ExecError{Code: "42804", Pos: o.offsetExpr.Pos(), Message: "OFFSET must be integer"}
		}
		o.offsetCount = v.Int
	}
	return nil
}

func (o *limitOp) Schema() planner.Schema { return o.child.Schema() }
func (o *limitOp) Close() error           { return o.child.Close() }

func (o *limitOp) Next() (TupleSlot, error) {
	for o.skipped < o.offsetCount {
		if _, err := o.child.Next(); err != nil {
			return nil, err
		}
		o.skipped++
	}
	if o.limitCount >= 0 && o.emitted >= o.limitCount {
		return nil, EOF
	}
	// M0069-0001 Stage C: pass-through.
	slot, err := o.child.Next()
	if err != nil {
		return nil, err
	}
	o.emitted++
	return slot, nil
}

// sortOp buffers the child's output then sorts under the supplied
// key list. Stable sort matches upstream's behaviour.
//
// M0068-0006: when the in-memory chunk exceeds sortChunkBytes the
// chunk is sorted, written to a spill file, and freed. After the
// child is fully drained an N-way merge over the spill files plus
// the in-memory tail produces the final ordered stream. This keeps
// peak heap residency bounded by the chunk size regardless of input
// row count, eliminating the heap blow-up that the M0066 review
// flagged for large sorts.
type sortOp struct {
	child Operator
	keys  []planner.SortKey
	ctx   *Context

	// chunk size threshold for triggering a spill. Default 256 MiB.
	chunkLimitBytes int64

	// In-memory chunk / tail.
	rows []Row
	idx  int

	// External-sort state. Populated only when at least one spill
	// has occurred during Open().
	spillFiles []string
	heap       *sortHeap
	mergeReady bool

	sortErr error
	outSlot MaterializedSlot // M0069-0001 Stage B per-op slot reuse
}

func newSortOp(plan *planner.Sort, child Operator) *sortOp {
	return &sortOp{child: child, keys: plan.Keys}
}

// sortChunkBytes is the in-memory threshold at which a sort chunk
// is flushed to a spill file. 256 MiB matches the build-side
// drainRowsBounded default and keeps a single chunk's footprint
// well below typical container-memory limits while remaining big
// enough to absorb every TPC-H SF=1 sort that doesn't otherwise
// require external sort.
const sortChunkBytes = int64(256 * 1024 * 1024)

func (o *sortOp) chunkLimit() int64 {
	if o.chunkLimitBytes > 0 {
		return o.chunkLimitBytes
	}
	return sortChunkBytes
}

func (o *sortOp) Open(ctx *Context) error {
	o.ctx = ctx
	if err := o.child.Open(ctx); err != nil {
		return err
	}
	limit := o.chunkLimit()
	var chunkBytes int64
	pulled := 0
	for {
		// M0062-followup: a sort over millions of rows can otherwise
		// drain the child without a cancel opportunity. ctx check
		// every 4096 rows pulled.
		if pulled&0xFFF == 0 && ctx != nil && ctx.Ctx != nil {
			if err := ctx.Ctx.Err(); err != nil {
				return &ExecError{Code: "57014", Message: "canceling statement due to user request"}
			}
		}
		row, err := NextRow(o.child)
		if err == EOF {
			break
		}
		if err != nil {
			return err
		}
		// M0069-0001 Stage B: Stage B made the slot's row
		// invalidated by the next Next() call; sort retains rows
		// across many calls, so clone before appending.
		o.rows = append(o.rows, cloneRow(row))
		chunkBytes += estimatedRowBytes(row)
		pulled++
		if chunkBytes >= limit {
			if err := o.flushChunk(); err != nil {
				return err
			}
			o.rows = o.rows[:0]
			chunkBytes = 0
		}
	}
	// Sort the final in-memory tail.
	o.sortChunk(o.rows)
	if o.sortErr != nil {
		return o.sortErr
	}
	return nil
}

// sortChunk in-place sorts a slice using the configured key list.
// Sets o.sortErr if an evaluator error surfaces during comparison.
func (o *sortOp) sortChunk(rows []Row) {
	sort.SliceStable(rows, func(i, j int) bool {
		return o.lessRows(rows[i], rows[j])
	})
}

// lessRows returns true iff a should sort before b under the
// configured key list. Records the first evaluator error in
// o.sortErr and returns false on error so the comparator stays
// strict-weak-ordered for the rest of the sort.
func (o *sortOp) lessRows(a, b Row) bool {
	for _, k := range o.keys {
		av, err := evalExpr(k.Expr, a, o.ctx)
		if err != nil {
			if o.sortErr == nil {
				o.sortErr = err
			}
			return false
		}
		bv, err := evalExpr(k.Expr, b, o.ctx)
		if err != nil {
			if o.sortErr == nil {
				o.sortErr = err
			}
			return false
		}
		if av.IsNull() && !bv.IsNull() {
			return !k.Desc
		}
		if !av.IsNull() && bv.IsNull() {
			return k.Desc
		}
		if av.IsNull() && bv.IsNull() {
			continue
		}
		cmp, err := compareDatum(av, bv, k.Expr.Pos())
		if err != nil {
			if o.sortErr == nil {
				o.sortErr = err
			}
			return false
		}
		if cmp == 0 {
			continue
		}
		if k.Desc {
			return cmp > 0
		}
		return cmp < 0
	}
	return false
}

// flushChunk sorts the current in-memory chunk and writes it to a
// new spill file. The caller must reset o.rows after the call.
func (o *sortOp) flushChunk() error {
	o.sortChunk(o.rows)
	if o.sortErr != nil {
		return o.sortErr
	}
	w, err := newSpillWriter(os.TempDir())
	if err != nil {
		return err
	}
	for _, r := range o.rows {
		if werr := w.WriteRow(r); werr != nil {
			w.Close()
			return werr
		}
	}
	if err := w.Close(); err != nil {
		return err
	}
	o.spillFiles = append(o.spillFiles, w.Path())
	return nil
}

func (o *sortOp) Schema() planner.Schema { return o.child.Schema() }
func (o *sortOp) Close() error {
	o.rows = nil
	o.idx = 0
	o.ctx = nil
	if o.heap != nil {
		for _, s := range o.heap.sources {
			if s.reader != nil {
				_ = s.reader.Close()
			}
		}
		o.heap = nil
	}
	for _, p := range o.spillFiles {
		_ = os.Remove(p)
	}
	o.spillFiles = nil
	return o.child.Close()
}

func (o *sortOp) Next() (TupleSlot, error) {
	if len(o.spillFiles) == 0 {
		// Fully in-memory path.
		if o.idx >= len(o.rows) {
			return nil, EOF
		}
		row := o.rows[o.idx]
		o.idx++
		return o.outSlot.set(row), nil
	}
	if !o.mergeReady {
		if err := o.initMerge(); err != nil {
			return nil, err
		}
	}
	row, err := o.popMerge()
	if err != nil {
		return nil, err
	}
	return o.outSlot.set(row), nil
}

// initMerge opens spill readers, primes each source with one row,
// and builds the min-heap.
func (o *sortOp) initMerge() error {
	o.heap = &sortHeap{less: o.lessRows}
	for _, p := range o.spillFiles {
		r, err := newSpillReader(p)
		if err != nil {
			return err
		}
		s := &sortSource{reader: r}
		if err := s.advance(); err != nil {
			return err
		}
		if !s.eof {
			heap.Push(o.heap, s)
		}
	}
	if len(o.rows) > 0 {
		s := &sortSource{rows: o.rows}
		s.advance()
		if !s.eof {
			heap.Push(o.heap, s)
		}
	}
	o.mergeReady = true
	return nil
}

// popMerge returns the smallest row across all sources, advancing
// the source it came from.
func (o *sortOp) popMerge() (Row, error) {
	if o.heap.Len() == 0 {
		return nil, EOF
	}
	s := heap.Pop(o.heap).(*sortSource)
	row := s.cur
	if err := s.advance(); err != nil {
		return nil, err
	}
	if !s.eof {
		heap.Push(o.heap, s)
	}
	if o.sortErr != nil {
		return nil, o.sortErr
	}
	return row, nil
}

// sortSource is a single input to the N-way merge. Either a
// spillReader (file-backed chunk) or an in-memory rows slice
// (the un-spilled tail).
type sortSource struct {
	reader *spillReader
	rows   []Row
	idx    int

	cur Row
	eof bool
}

func (s *sortSource) advance() error {
	if s.reader != nil {
		row, err := s.reader.ReadRow()
		if err == io.EOF {
			s.eof = true
			s.cur = nil
			_ = s.reader.Close()
			s.reader = nil
			return nil
		}
		if err != nil {
			return err
		}
		s.cur = cloneRow(row) // ReadRow's buffer is reused; clone for retain
		return nil
	}
	if s.idx >= len(s.rows) {
		s.eof = true
		s.cur = nil
		return nil
	}
	s.cur = s.rows[s.idx]
	s.idx++
	return nil
}

// sortHeap is a min-heap of sortSources keyed by their current row
// under a row-comparator function.
type sortHeap struct {
	sources []*sortSource
	less    func(a, b Row) bool
}

func (h *sortHeap) Len() int           { return len(h.sources) }
func (h *sortHeap) Less(i, j int) bool { return h.less(h.sources[i].cur, h.sources[j].cur) }
func (h *sortHeap) Swap(i, j int)      { h.sources[i], h.sources[j] = h.sources[j], h.sources[i] }
func (h *sortHeap) Push(x any)         { h.sources = append(h.sources, x.(*sortSource)) }
func (h *sortHeap) Pop() any {
	n := len(h.sources)
	x := h.sources[n-1]
	h.sources = h.sources[:n-1]
	return x
}
