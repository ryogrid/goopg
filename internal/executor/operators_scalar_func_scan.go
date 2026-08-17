package executor

// operators_scalar_func_scan.go — ScalarFuncScan operator.
// Returns one row by evaluating a scalar function expression.
// Used for `FROM parse_ident(...) AS a` and similar patterns. M0097-0003.

import "github.com/goopg/goopg/internal/optimizer"

type scalarFuncScanOp struct {
	plan *optimizer.ScalarFuncScan
	ctx  *Context
	done bool
}

func newScalarFuncScanOp(p *optimizer.ScalarFuncScan) *scalarFuncScanOp {
	return &scalarFuncScanOp{plan: p}
}

func (o *scalarFuncScanOp) Schema() optimizer.Schema { return o.plan.Output() }
func (o *scalarFuncScanOp) Open(ctx *Context) error { o.ctx = ctx; return nil }
func (o *scalarFuncScanOp) Close() error             { return nil }

func (o *scalarFuncScanOp) Next() (TupleSlot, error) {
	if o.done {
		return nil, EOF
	}
	o.done = true
	val, err := evalExpr(o.plan.Func, nil, o.ctx)
	if err != nil {
		return nil, err
	}
	return SlotFromRow(nil, Row{val}), nil
}
