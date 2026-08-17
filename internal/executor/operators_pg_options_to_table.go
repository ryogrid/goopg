package executor

// operators_pg_options_to_table.go — pg_options_to_table(text[]) SRF.
//
// Expands an option array (the on-disk reloptions/fdwoptions representation)
// into one row per element. Each element is "name=value" or a bare "name";
// the row is (option_name text, option_value text), with option_value NULL
// when the element carries no '='. Mirrors untransformRelOptions /
// pg_options_to_table in src/backend/foreign/foreign.c.
//
// pg_dump's getForeignDataWrappers calls this in a correlated subquery over
// pg_foreign_data_wrapper.fdwoptions; the operator therefore evaluates its
// argument against the current outer (lateral) row. DU-002 slice 17 (M0110-0001).

import (
	"strings"

	"github.com/goopg/goopg/internal/optimizer"
)

type pgOptionsToTableOp struct {
	plan *optimizer.PgOptionsToTable
	rows []Row
	idx  int
}

func newPgOptionsToTableOp(p *optimizer.PgOptionsToTable) *pgOptionsToTableOp {
	return &pgOptionsToTableOp{plan: p}
}

func (o *pgOptionsToTableOp) Schema() optimizer.Schema { return o.plan.Output() }

func (o *pgOptionsToTableOp) Open(ctx *Context) error {
	o.rows = nil
	o.idx = 0
	if o.plan.Arg == nil {
		return nil
	}

	// Resolve the argument against the current outer row when this SRF is
	// driven laterally (the pg_dump usage correlates on fdwoptions).
	var outerRow Row
	if len(ctx.OuterRows) > 0 {
		outerRow = ctx.OuterRows[len(ctx.OuterRows)-1]
	}
	argVal, err := evalExpr(o.plan.Arg, outerRow, ctx)
	if err != nil {
		return err
	}
	if argVal.IsNull() {
		return nil
	}

	for _, elem := range expandArrayDatum(argVal) {
		if elem.IsNull() {
			// A NULL option element has no name; PG's untransformRelOptions
			// would crash on it, and it never occurs in practice. Skip it.
			continue
		}
		if name, value, found := strings.Cut(elem.StringValue(), "="); found {
			o.rows = append(o.rows, Row{NewStringDatum(name), NewStringDatum(value)})
		} else {
			o.rows = append(o.rows, Row{NewStringDatum(name), NullDatum})
		}
	}
	return nil
}

func (o *pgOptionsToTableOp) Next() (TupleSlot, error) {
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	row := o.rows[o.idx]
	o.idx++
	return SlotFromRow(nil, row), nil
}

func (o *pgOptionsToTableOp) Close() error { return nil }
