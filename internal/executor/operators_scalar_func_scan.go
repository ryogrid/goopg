package executor

// operators_scalar_func_scan.go — ScalarFuncScan operator.
// Returns one row by evaluating a scalar function expression.
// Used for `FROM parse_ident(...) AS a` and similar patterns. M0097-0003.

import "github.com/goopg/goopg/internal/optimizer"

type scalarFuncScanOp struct {
	plan      *optimizer.ScalarFuncScan
	ctx       *Context
	outerSlot SlotView
	done      bool
}

func newScalarFuncScanOp(p *optimizer.ScalarFuncScan) *scalarFuncScanOp {
	return &scalarFuncScanOp{plan: p}
}

func (o *scalarFuncScanOp) Schema() optimizer.Schema { return o.plan.Output() }

// BindLateralOuter binds the outer row's slot for lateral arg evaluation.
// Called by joinOp before each per-outer-row Open when this scan sits on the
// right of a Join.Lateral == true (e.g. `FROM t, LATERAL f(t.col)` for a
// user-defined scalar/composite function). Mirrors pgGetSequenceDataOp.
// M0134-0126.
func (o *scalarFuncScanOp) BindLateralOuter(slot SlotView) { o.outerSlot = slot }

func (o *scalarFuncScanOp) Open(ctx *Context) error {
	o.ctx = ctx
	o.done = false
	return nil
}
func (o *scalarFuncScanOp) Close() error { return nil }

func (o *scalarFuncScanOp) Next() (TupleSlot, error) {
	if o.done {
		return nil, EOF
	}
	o.done = true
	val, err := evalExprSlot(o.plan.Func, o.outerSlot, o.ctx)
	if err != nil {
		return nil, err
	}
	sch := o.Schema()
	// When the schema has multiple columns (a composite/table-returning
	// non-SETOF routine, e.g. `FROM mki8(1,2)`), the datum is a composite
	// text "(v1,v2,...)" — decompose it into individual column values, same
	// as the SETOF sibling userSrfScanOp.Next(). M0134-0015c.
	if len(sch) > 1 && val.Kind == KindString {
		row := decomposeCompositeText(val.StringValue(), len(sch))
		return SlotFromRow(nil, row), nil
	}
	return SlotFromRow(nil, Row{val}), nil
}
