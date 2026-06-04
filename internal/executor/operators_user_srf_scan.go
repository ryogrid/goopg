package executor

import (
	"github.com/goopg/goopg/internal/planner"
)

// userSrfScanOp executes a user-defined SETOF function in the FROM clause.
// It calls evalSQLFunctionSetof / evalPLpgSQLFunctionSetof and emits each
// returned value as one row. M0097-0153.
type userSrfScanOp struct {
	plan *planner.UserSrfScan
	ctx  *Context
	rows []Datum
	idx  int
}

func newUserSrfScanOp(p *planner.UserSrfScan) *userSrfScanOp {
	return &userSrfScanOp{plan: p}
}

func (o *userSrfScanOp) Schema() planner.Schema {
	return o.plan.Output()
}

func (o *userSrfScanOp) Open(ctx *Context) error {
	o.ctx = ctx
	r := o.plan.Routine
	args := make([]Datum, len(o.plan.Args))
	for i, argExpr := range o.plan.Args {
		d, err := evalExprSlot(argExpr, nil, ctx)
		if err != nil {
			return err
		}
		args[i] = d
	}
	var rows []Datum
	var err error
	if r.Language == "plpgsql" {
		rows, err = evalPLpgSQLFunctionSetof(r, args, ctx, o.plan.Pos())
	} else {
		rows, err = evalSQLFunctionSetof(r, args, ctx, o.plan.Pos())
	}
	if err != nil {
		return err
	}
	o.rows = rows
	o.idx = 0
	return nil
}

func (o *userSrfScanOp) Close() error { return nil }

func (o *userSrfScanOp) Next() (TupleSlot, error) {
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	d := o.rows[o.idx]
	o.idx++
	sch := o.Schema()
	return asSlot(sch, Row{d}), nil
}
