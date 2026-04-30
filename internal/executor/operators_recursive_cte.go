package executor

import (
	"github.com/goopg/goopg/internal/planner"
)

// recursiveUnionOp executes a WITH RECURSIVE fixpoint (M0016-0004).
// It drains the anchor first, then iterates the recursive member with
// the current working table (set on ctx.WorkTableRows) until no more
// rows are produced.
type recursiveUnionOp struct {
	plan      *planner.RecursiveUnion
	anchor    Operator
	recursive Operator
	working   []Row
	output    []Row
	outIdx    int
	initDone  bool
	done      bool
	ctx       *Context
}

func newRecursiveUnionOp(p *planner.RecursiveUnion, anchor, recursive Operator) *recursiveUnionOp {
	return &recursiveUnionOp{plan: p, anchor: anchor, recursive: recursive}
}

func (o *recursiveUnionOp) Schema() planner.Schema { return o.plan.Output() }

func (o *recursiveUnionOp) Open(ctx *Context) error {
	o.ctx = ctx
	if err := o.anchor.Open(ctx); err != nil {
		return err
	}
	// Recursive member is opened fresh for each iteration.
	return nil
}

func (o *recursiveUnionOp) Close() error {
	if o.anchor != nil {
		o.anchor.Close()
	}
	return nil
}

func (o *recursiveUnionOp) Next() (Row, error) {
	if o.done {
		return nil, EOF
	}

	// Phase 1: drain the anchor.
	if !o.initDone {
		for {
			row, err := o.anchor.Next()
			if err == EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			r := make(Row, len(row))
			copy(r, row)
			o.working = append(o.working, r)
			o.output = append(o.output, r)
		}
		o.initDone = true
	}

	// Phase 2: iterate the fixpoint.
	for o.outIdx >= len(o.output) {
		if len(o.working) == 0 {
			o.done = true
			return nil, EOF
		}

		// Set the working table so WorkTableScanOp reads from it.
		o.ctx.WorkTableRows = o.working

		// Open and drain the recursive member.
		if err := o.recursive.Open(o.ctx); err != nil {
			return nil, err
		}
		iterRows := make([]Row, 0)
		for {
			row, err := o.recursive.Next()
			if err == EOF {
				break
			}
			if err != nil {
				o.recursive.Close()
				return nil, err
			}
			r := make(Row, len(row))
			copy(r, row)
			iterRows = append(iterRows, r)
		}
		o.recursive.Close()

		o.working = iterRows
		o.output = append(o.output, iterRows...)
		if len(iterRows) == 0 {
			o.done = true
			return nil, EOF
		}
	}

	row := o.output[o.outIdx]
	o.outIdx++
	return row, nil
}

// workTableScanOp reads rows from ctx.WorkTableRows, returning one
// row per Next() call. Used inside the recursive member of a
// RecursiveUnion.
type workTableScanOp struct {
	plan *planner.WorkTableScan
	ctx  *Context
	idx  int
}

func newWorkTableScanOp(p *planner.WorkTableScan) *workTableScanOp {
	return &workTableScanOp{plan: p}
}

func (o *workTableScanOp) Schema() planner.Schema { return o.plan.Output() }

func (o *workTableScanOp) Open(ctx *Context) error {
	o.ctx = ctx
	o.idx = 0
	return nil
}

func (o *workTableScanOp) Close() error { return nil }

func (o *workTableScanOp) Next() (Row, error) {
	if o.ctx == nil || o.idx >= len(o.ctx.WorkTableRows) {
		return nil, EOF
	}
	row := o.ctx.WorkTableRows[o.idx]
	o.idx++
	return row, nil
}
