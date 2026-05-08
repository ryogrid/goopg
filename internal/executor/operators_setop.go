package executor

import (
	"github.com/goopg/goopg/internal/planner"
)

// setOp executes UNION ALL by draining the left side then the right.
type setOp struct {
	plan      *planner.SetOp
	left      Operator
	right     Operator
	leftDone  bool
	rightDone bool
	opened    bool
	outSlot MaterializedSlot
}

func newSetOp(p *planner.SetOp, left, right Operator) *setOp {
	return &setOp{plan: p, left: left, right: right}
}

func (o *setOp) Schema() planner.Schema {
	return o.plan.Output()
}

func (o *setOp) Open(ctx *Context) error {
	if err := o.left.Open(ctx); err != nil {
		return err
	}
	if err := o.right.Open(ctx); err != nil {
		o.left.Close()
		return err
	}
	o.opened = true
	return nil
}

func (o *setOp) Close() error {
	var lErr, rErr error
	if o.left != nil {
		lErr = o.left.Close()
	}
	if o.right != nil {
		rErr = o.right.Close()
	}
	if lErr != nil {
		return lErr
	}
	return rErr
}

func (o *setOp) Next() (TupleSlot, error) {
	if !o.leftDone {
		row, err := NextRow(o.left)
		if err == EOF {
			o.leftDone = true
			o.left.Close()
		} else if err != nil {
			return nil, err
		} else {
			return o.outSlot.set(row), nil
		}
	}
	if !o.rightDone {
		row, err := NextRow(o.right)
		if err == EOF {
			o.rightDone = true
			o.right.Close()
		} else if err != nil {
			return nil, err
		} else {
			return o.outSlot.set(row), nil
		}
	}
	return nil, EOF
}
