package executor

// operators_from_unnest.go — FROM unnest(array_expr) alias(col).
// Expands an array expression into one row per element.
// M0097-0035.

import (
	"github.com/goopg/goopg/internal/planner"
)

type fromUnnestOp struct {
	plan *planner.FromUnnest
	ctx  *Context
	rows []Row
	idx  int
}

func newFromUnnestOp(p *planner.FromUnnest) *fromUnnestOp {
	return &fromUnnestOp{plan: p}
}

func (o *fromUnnestOp) Schema() planner.Schema { return o.plan.Output() }

func (o *fromUnnestOp) Open(ctx *Context) error {
	o.ctx = ctx
	o.idx = 0
	o.rows = nil

	// Evaluate the array expression using outer rows if this is lateral.
	var outerRow Row
	if len(ctx.OuterRows) > 0 {
		outerRow = ctx.OuterRows[len(ctx.OuterRows)-1]
	}
	arrD, err := evalExpr(o.plan.ArrExpr, outerRow, ctx)
	if err != nil {
		return err
	}
	if arrD.IsNull() {
		return nil
	}

	// Expand the array into individual element rows.
	elems := expandArrayDatum(arrD)
	o.rows = make([]Row, len(elems))
	for i, elem := range elems {
		o.rows[i] = Row{elem}
	}
	return nil
}

func (o *fromUnnestOp) Close() error { return nil }

func (o *fromUnnestOp) Next() (TupleSlot, error) {
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	row := o.rows[o.idx]
	o.idx++
	return SlotFromRow(nil, row), nil
}

// expandArrayDatum parses a Datum containing an array and returns its elements.
func expandArrayDatum(d Datum) []Datum {
	var elems []Datum
	switch d.Kind {
	case KindString:
		// Text array format: {elem1,elem2,...}
		parts := parseTextArray(d.StringValue())
		for _, p := range parts {
			if p == "NULL" {
				elems = append(elems, NullDatum)
			} else {
				elems = append(elems, NewStringDatum(p))
			}
		}
	case KindBytes:
		// Bytes stored as text array in varlena format.
		parts := parseTextArray(string(d.BytesValue()))
		for _, p := range parts {
			if p == "NULL" {
				elems = append(elems, NullDatum)
			} else {
				elems = append(elems, NewStringDatum(p))
			}
		}
	}
	return elems
}
