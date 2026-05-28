package executor

// operators_project_set.go — ProjectSet operator: evaluate one set-returning
// function call per child row and emit each row of its composite result as
// one output row. Closes the libpqrcv `fetch_table_list` probe-survival gap
// for the goopg primary + PG subscriber path: the probe shape
// `(pg_get_publication_tables(VARIADIC array_agg(pubname::text))).*` plans
// as `Aggregate → ProjectSet(srf(<agg-output-col>))` and ProjectSet expands
// the SRF's three-column composite over each child row. M0103-0008.
//
// M0097-0045: Extended to handle generate_series() in SELECT target list.
// When plan.SrfCols is non-empty, the operator expands each SRF per child
// row and zips multiple SRFs together (parallel expansion with NULL padding).

import (
	"strings"

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

	// SELECT-list SRF mode: generate_series() or user SETOF functions in target list.
	if len(o.plan.SrfCols) > 0 || len(o.plan.UserSrfCols) > 0 {
		return o.openSelectSrfMode(ctx)
	}

	// Legacy composite-SRF mode (pg_get_publication_tables).
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

// openSelectSrfMode handles the generate_series()-in-SELECT-list expansion.
// For each child row:
//  1. Evaluate all non-SRF (passthrough) expressions.
//  2. Evaluate start/stop/step for each SRF column.
//  3. Zip: emit one output row per step, repeating passthrough values.
//     If multiple SRFs have different lengths, NULL-pad the shorter ones.
func (o *projectSetOp) openSelectSrfMode(ctx *Context) error {
	for {
		slot, err := o.child.Next()
		if err == EOF {
			break
		}
		if err != nil {
			return err
		}
		childRow := slotRow(slot)

		// 1. Evaluate non-SRF (passthrough) expressions.
		n := len(o.plan.OtherExprs)
		otherVals := make([]Datum, n)
		for j, expr := range o.plan.OtherExprs {
			if expr == nil {
				continue // SRF slot — filled below
			}
			d, err := evalExpr(expr, childRow, ctx)
			if err != nil {
				return err
			}
			otherVals[j] = d
		}

		// 2. Evaluate each generate_series SRF and collect series values.
		type seriesResult struct {
			colIdx int
			vals   []int64
		}
		genResults := make([]seriesResult, len(o.plan.SrfCols))
		maxLen := 0
		for k, sc := range o.plan.SrfCols {
			startD, err := evalExpr(sc.Start, childRow, ctx)
			if err != nil {
				return err
			}
			stopD, err := evalExpr(sc.Stop, childRow, ctx)
			if err != nil {
				return err
			}
			start, _ := datumInt64(startD)
			stop, _ := datumInt64(stopD)
			step := int64(1)
			if sc.Step != nil {
				stepD, err := evalExpr(sc.Step, childRow, ctx)
				if err != nil {
					return err
				}
				s, ok := datumInt64(stepD)
				if ok {
					step = s
				}
			}
			var vals []int64
			if step > 0 {
				for v := start; v <= stop; v += step {
					vals = append(vals, v)
				}
			} else if step < 0 {
				for v := start; v >= stop; v += step {
					vals = append(vals, v)
				}
			}
			genResults[k] = seriesResult{colIdx: sc.ColIdx, vals: vals}
			if len(vals) > maxLen {
				maxLen = len(vals)
			}
		}

		// Evaluate user-defined SETOF SQL functions. M0097-0020.
		type userSrfResult struct {
			colIdx int
			vals   []Datum
		}
		userResults := make([]userSrfResult, len(o.plan.UserSrfCols))
		for k, usc := range o.plan.UserSrfCols {
			args := make([]Datum, len(usc.Args))
			for j, arg := range usc.Args {
				d, err := evalExpr(arg, childRow, ctx)
				if err != nil {
					return err
				}
				args[j] = d
			}
			var (
				vals   []Datum
				srfErr error
			)
			if strings.EqualFold(usc.Routine.Language, "plpgsql") {
				vals, srfErr = evalPLpgSQLFunctionSetof(usc.Routine, args, ctx, usc.FuncPos)
			} else {
				vals, srfErr = evalSQLFunctionSetof(usc.Routine, args, ctx, usc.FuncPos)
			}
			if srfErr != nil {
				return srfErr
			}
			userResults[k] = userSrfResult{colIdx: usc.ColIdx, vals: vals}
			if len(vals) > maxLen {
				maxLen = len(vals)
			}
		}

		// 3. Zip: emit maxLen rows, null-padding shorter series.
		for step := 0; step < maxLen; step++ {
			outRow := make(Row, n)
			copy(outRow, otherVals)
			for _, sr := range genResults {
				if step < len(sr.vals) {
					outRow[sr.colIdx] = NewIntDatum(sr.vals[step])
				} else {
					outRow[sr.colIdx] = Datum{} // NULL
				}
			}
			for _, ur := range userResults {
				if step < len(ur.vals) {
					outRow[ur.colIdx] = ur.vals[step]
				} else {
					outRow[ur.colIdx] = Datum{} // NULL
				}
			}
			o.rows = append(o.rows, outRow)
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
