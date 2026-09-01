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
	plan      *optimizer.PgOptionsToTable
	outerSlot SlotView
	rows      []Row
	idx       int
}

func newPgOptionsToTableOp(p *optimizer.PgOptionsToTable) *pgOptionsToTableOp {
	return &pgOptionsToTableOp{plan: p}
}

func (o *pgOptionsToTableOp) Schema() optimizer.Schema { return o.plan.Output() }

// BindLateralOuter binds the outer row's slot for lateral arg evaluation,
// mirroring the sibling lateral SRFs (pg_get_sequence_data,
// pg_get_publication_tables, verify_heapam). Without it this op was not
// lateralBindable, so under an explicit `LATERAL pg_options_to_table(z.opts)`
// the argument's same-level ColumnRef had no slot to resolve against and the
// query died with "column ref opts/1 on nil slot" (review/260831-2 EO2-6).
// Passing nil clears the binding.
func (o *pgOptionsToTableOp) BindLateralOuter(slot SlotView) { o.outerSlot = slot }

func (o *pgOptionsToTableOp) Open(ctx *Context) error {
	o.rows = nil
	o.idx = 0
	if o.plan.Arg == nil {
		return nil
	}

	// Resolve the argument against the bound lateral outer slot when there is
	// one (explicit LATERAL / implicit-LATERAL comma join), else against the
	// correlation stack — the pg_dump usage correlates on fdwoptions from a
	// subquery, where only ctx.OuterRows is set.
	if o.outerSlot == nil && len(ctx.OuterRows) > 0 {
		o.outerSlot = SlotFromRow(nil, ctx.OuterRows[len(ctx.OuterRows)-1])
	}
	argVal, err := evalExprSlot(o.plan.Arg, o.outerSlot, ctx)
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
