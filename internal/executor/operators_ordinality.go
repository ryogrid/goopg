package executor

// operators_ordinality.go — WITH ORDINALITY and ROWS FROM executors.
// OrdinalityWrap appends an int8 row-counter (1-based) to any child SRF.
// RowsFromOp zips multiple SRF children side-by-side (NULL-pads shorter).

import (
	"github.com/goopg/goopg/internal/planner"
)

// ordinalityOp wraps a child operator, appending a bigint ordinality counter.
type ordinalityOp struct {
	plan  *planner.OrdinalityWrap
	child Operator
	ord   int64
}

func newOrdinalityOp(p *planner.OrdinalityWrap, child Operator) *ordinalityOp {
	return &ordinalityOp{plan: p, child: child}
}

func (o *ordinalityOp) Schema() planner.Schema { return o.plan.Output() }

func (o *ordinalityOp) Open(ctx *Context) error {
	o.ord = 0
	return o.child.Open(ctx)
}

func (o *ordinalityOp) Close() error { return o.child.Close() }

func (o *ordinalityOp) Next() (TupleSlot, error) {
	slot, err := o.child.Next()
	if err != nil {
		return nil, err
	}
	o.ord++
	childRow := slotRow(slot)
	row := make(Row, len(childRow)+1)
	copy(row, childRow)
	row[len(childRow)] = NewIntDatum(o.ord)
	return SlotFromRow(nil, row), nil
}

// rowsFromOp zips multiple child SRF operators side-by-side.
// When one child is exhausted, its columns are NULL-padded.
type rowsFromOp struct {
	plan      *planner.RowsFrom
	children  []Operator
	widths    []int  // number of output columns per child
	exhausted []bool // true once a child returned EOF
}

func newRowsFromOp(p *planner.RowsFrom, children []Operator) *rowsFromOp {
	widths := make([]int, len(children))
	for i, c := range children {
		widths[i] = len(c.Schema())
	}
	return &rowsFromOp{
		plan:      p,
		children:  children,
		widths:    widths,
		exhausted: make([]bool, len(children)),
	}
}

func (o *rowsFromOp) Schema() planner.Schema { return o.plan.Output() }

func (o *rowsFromOp) Open(ctx *Context) error {
	for i := range o.exhausted {
		o.exhausted[i] = false
	}
	for _, c := range o.children {
		if err := c.Open(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (o *rowsFromOp) Close() error {
	for _, c := range o.children {
		c.Close()
	}
	return nil
}

func (o *rowsFromOp) Next() (TupleSlot, error) {
	// If all children are exhausted, we are done.
	allExhausted := true
	for _, ex := range o.exhausted {
		if !ex {
			allExhausted = false
			break
		}
	}
	if allExhausted {
		return nil, EOF
	}
	// Advance each non-exhausted child and build combined row.
	var combined Row
	anyRowFetched := false
	for i, child := range o.children {
		if o.exhausted[i] {
			// Pad with NULLs for this child's columns.
			for range o.widths[i] {
				combined = append(combined, NullDatum)
			}
			continue
		}
		slot, err := child.Next()
		if err == EOF {
			o.exhausted[i] = true
			for range o.widths[i] {
				combined = append(combined, NullDatum)
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		anyRowFetched = true
		combined = append(combined, slotRow(slot)...)
	}
	if !anyRowFetched {
		return nil, EOF
	}
	return SlotFromRow(nil, combined), nil
}
