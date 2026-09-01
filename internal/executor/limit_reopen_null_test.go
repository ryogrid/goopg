package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// limitRowsOp replays the same rows on every Open, standing in for the inner
// side of a correlated subplan that is rescanned once per outer row.
type limitRowsOp struct {
	schema optimizer.Schema
	rows   []Row
	next   int
}

func (o *limitRowsOp) Open(*Context) error        { o.next = 0; return nil }
func (o *limitRowsOp) Schema() optimizer.Schema   { return o.schema }
func (o *limitRowsOp) Close() error               { return nil }
func (o *limitRowsOp) Next() (TupleSlot, error) { //nolint:ireturn
	if o.next >= len(o.rows) {
		return nil, EOF
	}
	r := o.rows[o.next]
	o.next++
	return SlotFromRow(o.schema, r), nil
}

// TestLimitOpReOpenWithNullLimitDropsPreviousBound is the review/260831-2 EO1-2
// guard. A correlated LIMIT/OFFSET (`SELECT (SELECT ... LIMIT o.n) FROM o`) is
// re-evaluated on every Open, and a NULL result means "no limit". limitOp's
// NULL arms only skipped the assignment, so the bound from the PREVIOUS outer
// row stayed in place and the supposedly unlimited execution kept returning
// that earlier row count.
func TestLimitOpReOpenWithNullLimitDropsPreviousBound(t *testing.T) {
	schema := optimizer.Schema{{Name: "a"}}
	child := &limitRowsOp{schema: schema, rows: []Row{
		{NewIntDatum(1)}, {NewIntDatum(2)}, {NewIntDatum(3)}, {NewIntDatum(4)},
	}}
	op := &limitOp{
		child:      child,
		limitExpr:  &optimizer.ParamRef{Number: 1},
		offsetExpr: &optimizer.ParamRef{Number: 2},
		limitCount: -1,
	}

	count := func(ctx *Context) int {
		t.Helper()
		if err := op.Open(ctx); err != nil {
			t.Fatalf("Open: %v", err)
		}
		n := 0
		for {
			slot, err := op.Next()
			if err == EOF {
				break
			}
			if err != nil {
				t.Fatalf("Next: %v", err)
			}
			if slot == nil {
				break
			}
			n++
		}
		return n
	}

	bounded := &Context{Params: []Datum{NewIntDatum(2), NewIntDatum(1)}}
	if got := count(bounded); got != 2 {
		t.Fatalf("bounded execution returned %d rows, want 2 (LIMIT 2 OFFSET 1)", got)
	}

	// Same operator, next outer row: both bounds are NULL, i.e. unlimited from
	// the first row.
	unbounded := &Context{Params: []Datum{NullDatum, NullDatum}}
	if got := count(unbounded); got != 4 {
		t.Errorf("re-Open with NULL LIMIT/OFFSET returned %d rows, want 4: the previous row's bound was not cleared", got)
	}
}
