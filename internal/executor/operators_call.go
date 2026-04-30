package executor

import (
	"github.com/goopg/goopg/internal/planner"
)

// callOp executes `CALL proc(...)` (M0015 Stage B).
type callOp struct {
	plan *planner.Call
	ctx  *Context
	done bool
}

func newCallOp(p *planner.Call) *callOp {
	return &callOp{plan: p}
}

func (o *callOp) Schema() planner.Schema { return nil }
func (o *callOp) Open(ctx *Context) error {
	o.ctx = ctx
	return nil
}
func (o *callOp) Close() error { return nil }

func (o *callOp) Next() (Row, error) {
	if o.done {
		return nil, EOF
	}
	o.done = true
	return nil, &ExecError{Code: "0A000", Pos: o.plan.Stmt.Pos(),
		Message: "CALL is not yet implemented in v0 (Stage B)"}
}
