package executor

// operators_project_set.go — ProjectSet operator: evaluate one set-returning
// function call per child row and emit each row of its composite result as
// one output row. Closes the libpqrcv `fetch_table_list` probe-survival gap
// for the goopg primary + PG subscriber path: the probe shape
// `(pg_get_publication_tables(VARIADIC array_agg(pubname::text))).*` plans
// as `Aggregate → ProjectSet(srf(<agg-output-col>))` and ProjectSet expands
// the SRF's three-column composite over each child row. M0103-0008.

import (
	"github.com/goopg/goopg/internal/planner"
)

type projectSetOp struct {
	plan   *planner.ProjectSet
	child  Operator
	schema planner.Schema

	ctx  *Context
	rows []Row
	pos  int
}

func newProjectSetOp(p *planner.ProjectSet, child Operator) *projectSetOp {
	return &projectSetOp{plan: p, child: child, schema: p.Output()}
}

func (o *projectSetOp) Schema() planner.Schema { return o.schema }

func (o *projectSetOp) Open(ctx *Context) error {
	o.ctx = ctx
	o.rows = nil
	o.pos = 0
	if err := o.child.Open(ctx); err != nil {
		return err
	}
	for {
		slot, err := o.child.Next()
		if err == EOF {
			break
		}
		if err != nil {
			return err
		}
		row := slotRow(slot)
		args := make([]Datum, 0, len(o.plan.SrfArgs))
		for _, a := range o.plan.SrfArgs {
			d, err := evalExpr(a, row, ctx)
			if err != nil {
				return err
			}
			args = append(args, d)
		}
		switch o.plan.SrfName {
		case "pg_get_publication_tables":
			sub, err := buildPgGetPublicationTablesRows(ctx, args)
			if err != nil {
				return err
			}
			o.rows = append(o.rows, sub...)
		default:
			return &ExecError{Code: "0A000",
				Message: "ProjectSet: unsupported set-returning function " + o.plan.SrfName}
		}
	}
	return nil
}

func (o *projectSetOp) Close() error { return o.child.Close() }

func (o *projectSetOp) Next() (TupleSlot, error) {
	if o.pos >= len(o.rows) {
		return nil, EOF
	}
	row := o.rows[o.pos]
	o.pos++
	return SlotFromRow(nil, row), nil
}
