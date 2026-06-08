package executor

import (
	"strings"

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
	// When the schema has multiple OUT-parameter columns, the datum is a
	// composite text "(v1,v2,...)" — decompose it into individual column values.
	if len(sch) > 1 && d.Kind == KindString {
		row := decomposeCompositeText(d.StringValue(), len(sch))
		return asSlot(sch, row), nil
	}
	return asSlot(sch, Row{d}), nil
}

// decomposeCompositeText splits a PostgreSQL composite literal "(v1,v2,...)"
// into n Datum values. Empty fields become NullDatum.
func decomposeCompositeText(s string, n int) Row {
	// Strip outer parens.
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '(' && s[len(s)-1] == ')' {
		s = s[1 : len(s)-1]
	}
	// Simple comma-split (no quoted-string handling — sufficient for
	// text/int/regclass values that don't contain commas).
	parts := strings.SplitN(s, ",", n)
	row := make(Row, n)
	for i := range row {
		if i < len(parts) && parts[i] != "" {
			row[i] = NewStringDatum(parts[i])
		} else {
			row[i] = NullDatum
		}
	}
	return row
}
