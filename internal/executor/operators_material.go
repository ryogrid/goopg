package executor

// Materialize — PG's nodeMaterial.c analogue (M0127-P4.3; design
// leftdeep-joins/07 §4).
//
// The operator that makes a subtree *rescannable* without re-executing it. Its
// first pass reads the child once and caches every row it hands upward; a
// Rescan replays that cache from the start. A nested loop can therefore stream
// its outer side one tuple at a time and still walk the whole inner side per
// outer tuple, which is what removes the "drain both sides into two []Row
// before joining anything" shape the executor has had since M0036.
//
// Two properties are copied deliberately from PG:
//
//   - The cache fills LAZILY, exactly as far as the consumer pulled. PG's
//     ExecMaterial keeps `eof_underlying` false until the subplan really ends
//     and, on a rescan that runs past the stored rows, resumes reading the
//     subplan and storing the extra rows. goopg needs the same because a
//     keyless semi/anti join breaks out of its inner scan on the first
//     qualifying row: the next outer row must be able to continue past that
//     point rather than see a truncated inner side.
//   - The resident set answers to `work_mem`. The prefix that fits stays in
//     memory; the remainder goes to one spill file that replays sequentially
//     (`materialBuffer`, below). PG uses a tuplestore for the same reason.
//
// What is NOT here yet: a `planner.Materialize` plan node, its path, and the
// EXPLAIN line. In PG a Material node is visible in EXPLAIN output and is
// placed by cost_rescan when the inner side has no cheap rescan of its own.
// goopg cannot make that placement decision faithfully before doc 04's cost
// currency exists, and emitting the node unconditionally would move EXPLAIN
// text away from PG on every plan where PG declines it. The operator is
// therefore constructed by the executor at the NL inner side for now and the
// plan node is P5.4's (deferral ledger, 2026-08-04 M0127-P4.3).

import (
	"fmt"
	"io"

	"github.com/goopg/goopg/internal/planner"
)

// rescannable is implemented by operators that can replay their output from
// the beginning without re-executing their subtree — PG's ExecReScan family.
// A consumer that owns the operator (the NL join owns its Materialize) calls
// Rescan between passes; everything else keeps using the plain Operator
// contract.
type rescannable interface {
	Operator
	// Rescan repositions the operator at the start of its output. It must be
	// valid to call it before the previous pass reached EOF.
	Rescan() error
}

// materialBuffer is a replayable row cache bounded by work_mem: the prefix
// that fits stays resident, the overflow lives in one spill file that is
// replayed sequentially.
//
// This is the mechanism `mergeJoinStream.bufferGroup` grew by hand in P4.1 —
// its ledger row #3 scheduled the extraction for whichever slice needed it
// second, and Materialize is that slice. Callers walk `rowAt` upward from 0,
// so the overflow portion is a single sequential pass per replay; asking for
// the first overflow row rewinds the reader, which is PG's ExecRestrPos in the
// only form goopg's spill files support.
type materialBuffer struct {
	ctx   *Context
	limit int64 // work_mem in bytes; <= 0 means unlimited

	mem   []Row
	bytes int64
	count int // total rows appended, resident + spilled

	w     *spillWriter
	path  string
	r     *spillReader
	dirty bool // rows written since the last Flush
}

func (b *materialBuffer) reset(ctx *Context) {
	b.close()
	b.ctx = ctx
	b.limit = 0
	if ctx != nil {
		b.limit = ctx.WorkMem
	}
	b.mem = b.mem[:0]
	b.bytes = 0
	b.count = 0
	b.dirty = false
}

// append stores one row. The row must already be owned by the caller (the
// buffer retains it).
func (b *materialBuffer) append(row Row) error {
	b.count++
	size := estimatedRowBytes(row)
	// Always keep at least one row resident: a budget smaller than a single
	// row must still make progress rather than spill every row.
	if b.w == nil && (b.limit <= 0 || len(b.mem) == 0 || b.bytes+size <= b.limit) {
		b.mem = append(b.mem, row)
		b.bytes += size
		return nil
	}
	if b.w == nil {
		w, err := newSpillWriter(b.ctx)
		if err != nil {
			return err
		}
		b.w = w
		b.path = w.Path()
		// The reader is opened HERE, with the file still empty, rather than
		// lazily at the first overflow read. P3.3's ledger row named the
		// hazard: the temp-file registry's release point is the statement,
		// PG's is the resource owner, and a Materialize is the first operator
		// whose cache can outlive the pull that filled it. An fd opened up
		// front survives an unlink; a path resolved later does not.
		r, err := newSpillReader(b.path)
		if err != nil {
			return err
		}
		b.r = r
	}
	if err := b.w.WriteRow(row); err != nil {
		return err
	}
	b.dirty = true
	return nil
}

// rowAt returns the i-th appended row. i must be < count and callers must walk
// upward; i == len(mem) rewinds the overflow reader.
func (b *materialBuffer) rowAt(i int) (Row, error) {
	if i < len(b.mem) {
		return b.mem[i], nil
	}
	if b.w == nil {
		return nil, fmt.Errorf("internal error: materialize buffer has no overflow file for row %d of %d", i, b.count)
	}
	// The writer stays OPEN across replays: unlike the merge join's group
	// buffer, a Materialize can be rescanned and then asked to grow (a
	// short-circuited first pass, see the file header). So the reader is a
	// second descriptor on the same path and the writer is flushed whenever
	// rows have been added since the last read.
	if b.dirty {
		if err := b.w.Flush(); err != nil {
			return nil, err
		}
		b.dirty = false
	}
	if i == len(b.mem) {
		if err := b.r.rewind(); err != nil {
			return nil, err
		}
	}
	row, err := b.r.ReadRow()
	if err == io.EOF {
		// count tracks what append wrote, so running out of file early is a
		// bookkeeping bug, not the end of anything. (io.EOF is NOT the
		// executor's EOF sentinel, so it could never have been mistaken for
		// the end of the operator's output.)
		return nil, fmt.Errorf("internal error: materialize overflow file ended at row %d of %d", i, b.count)
	}
	if err != nil {
		return nil, err
	}
	return row, nil
}

func (b *materialBuffer) close() {
	b.mem = nil
	b.bytes = 0
	if b.r != nil {
		b.r.Close()
		b.r = nil
	}
	if b.w != nil {
		b.w.Close()
		b.w = nil
	}
	if b.path != "" {
		if b.ctx != nil {
			b.ctx.removeSpillFile(b.path)
		}
		b.path = ""
	}
	b.count = 0
	b.dirty = false
}

// materializeOp caches its child's output so the subtree can be replayed.
type materializeOp struct {
	child  Operator
	ctx    *Context
	schema planner.Schema

	buf materialBuffer
	pos int
	// out is the one slot this operator ever returns. A Materialize under a
	// nested loop is stepped N×M times, so allocating a MaterializedSlot per
	// call would put an allocation on the join's innermost loop — the exact
	// cost `concatRows`-per-pair was removed to avoid. It is a PRODUCER slot
	// in the sense operator.go documents: a consumer that retains it across
	// Next must Materialize.
	out MaterializedSlot
	// eofUnderlying mirrors PG's MaterialState field of the same name: once
	// the child has returned EOF the cache is complete and no further pass
	// touches the child again.
	eofUnderlying bool
}

var _ rescannable = (*materializeOp)(nil)

func newMaterializeOp(child Operator) *materializeOp {
	return &materializeOp{child: child}
}

func (o *materializeOp) Open(ctx *Context) error {
	o.ctx = ctx
	o.buf.reset(ctx)
	o.pos = 0
	o.eofUnderlying = false
	o.schema = o.child.Schema()
	if err := o.child.Open(ctx); err != nil {
		return err
	}
	// A child whose Schema() only becomes known after Open (subplans built
	// without one) gets a second chance here.
	if len(o.schema) == 0 {
		o.schema = o.child.Schema()
	}
	return nil
}

// openCached prepares the cache over an ALREADY-OPEN child, for a consumer
// that owns its children's lifecycle itself (the NL join opens and closes both
// of joinOp's children). Pair it with releaseCache, never with Close.
func (o *materializeOp) openCached(ctx *Context) {
	o.ctx = ctx
	o.buf.reset(ctx)
	o.pos = 0
	o.eofUnderlying = false
	o.schema = o.child.Schema()
}

// releaseCache frees the cache and its spill file without closing the child.
func (o *materializeOp) releaseCache() { o.buf.close() }

func (o *materializeOp) Schema() planner.Schema { return o.schema }

func (o *materializeOp) Next() (TupleSlot, error) {
	if o.pos < o.buf.count {
		row, err := o.buf.rowAt(o.pos)
		if err != nil {
			return nil, err
		}
		o.pos++
		return o.slot(row), nil
	}
	if o.eofUnderlying {
		return nil, EOF
	}
	slot, err := o.child.Next()
	if err == EOF {
		o.eofUnderlying = true
		return nil, EOF
	}
	if err != nil {
		return nil, err
	}
	// The buffer retains the row across the child's next step, so it has to
	// own its bytes — the same retention boundary drainRowsBounded documents.
	row := slotRow(slot)
	var dup Row
	if rowHasArena(row) {
		dup = cloneRowOwned(row)
	} else {
		dup = make(Row, len(row))
		copy(dup, row)
	}
	if err := o.buf.append(dup); err != nil {
		return nil, err
	}
	o.pos++
	return o.slot(dup), nil
}

// slot re-points the reusable output slot at row.
func (o *materializeOp) slot(row Row) TupleSlot { //nolint:ireturn
	o.out.schema = o.schema
	o.out.row = row
	return &o.out
}

// Rescan repositions at the first cached row. The child is untouched: that is
// the entire point of the operator.
func (o *materializeOp) Rescan() error {
	o.pos = 0
	return nil
}

func (o *materializeOp) Close() error {
	o.buf.close()
	return o.child.Close()
}

// setUnbounded drops the work_mem budget for this cache, so it never spills.
// Callers use it when the replay cost of a spilled cache is not yet priced by
// the planner — see joinOp.openNestedLoop.
func (o *materializeOp) setUnbounded() { o.buf.limit = 0 }
