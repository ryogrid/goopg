package executor

import (
	"github.com/goopg/goopg/internal/optimizer"
)

// compiledCondDisabled is the A/B and equivalence-test knob for C43: when
// set, the scans evaluate their Cond with the interpreter again.
var compiledCondDisabled = false

// compiledCond is a scan node's Cond compiled once for evalFastExpr
// (M0122-0012 C43). The index, index-only and bitmap heap scans evaluated
// their per-row filter with the interpreted evalExpr — boxing the row into a
// fresh slot and walking the expression tree for every fetched tuple — while
// the seq scan's absorbed qual and the join residual (joinPredicateMatch,
// slice 45) run compiled. TPC-DS Q14's date_dim probes (`d_year = 2001 AND
// d_moy = 11`, `d_week_seq = (InitPlan)`) spent 7 % of the query there. PG
// evaluates a scan qual through ExecQual over a compiled ExprState
// (ExecInitQual, execExpr.c). The slab is built without constant folding on
// first use: the plan is fixed for the operator's life and the slab holds no
// Context state. evalFastExpr delegates every kind it does not compile back
// to evalExprSlot, so the result is the interpreter's.
type compiledCond struct {
	exprs exprTreeSlab
	idx   int32
	done  bool
	slot  MaterializedSlot
}

// eval evaluates cond against row.
func (c *compiledCond) eval(cond optimizer.Expr, row Row, ctx *Context) (Datum, error) {
	if compiledCondDisabled {
		return evalExpr(cond, row, ctx)
	}
	if !c.done {
		c.exprs = c.exprs[:0]
		c.idx = c.exprs.buildExpr(cond)
		c.done = true
	}
	c.slot.row = row
	v, err := evalFastExpr(c.exprs, c.idx, &c.slot, ctx)
	c.slot.row = nil
	return v, err
}
