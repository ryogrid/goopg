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
	"math"
	"strings"

	"github.com/goopg/goopg/internal/optimizer"
)

type projectSetOp struct {
	plan   *optimizer.ProjectSet
	child  Operator
	schema optimizer.Schema

	ctx  *Context
	rows []Row
	pos  int
}

func newProjectSetOp(p *optimizer.ProjectSet, child Operator) *projectSetOp {
	return &projectSetOp{plan: p, child: child, schema: p.Output()}
}

func (o *projectSetOp) Schema() optimizer.Schema { return o.schema }

func (o *projectSetOp) Open(ctx *Context) error {
	o.ctx = ctx
	o.rows = nil
	o.pos = 0
	if err := o.child.Open(ctx); err != nil {
		return err
	}

	// SELECT-list SRF mode: generate_series() / unnest() / regexp_matches() / user SETOF functions in target list.
	if len(o.plan.SrfCols) > 0 || len(o.plan.UnnestCols) > 0 || len(o.plan.RegexpMatchesCols) > 0 || len(o.plan.UserSrfCols) > 0 {
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
			colIdx  int
			vals    []int64
			wrapped bool // raw value goes into the eval row, not the output row
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
			// review/260831-2 EO2-3: a zero step used to yield an empty
			// series instead of PG's error, and `v += step` wraps at the
			// int64 ceiling — the wrapped value is back inside the bounds, so
			// the loop appended forever and the backend grew without limit.
			// PG raises for step 0 (int8.c generate_series_int8,
			// ERRCODE_INVALID_PARAMETER_VALUE) and ends the series when the
			// addition overflows (pg_add_s64_overflow).
			if step == 0 {
				return &ExecError{Code: "22023", Message: "step size cannot equal zero"}
			}
			var vals []int64
			if step > 0 {
				for v := start; v <= stop; v += step {
					vals = append(vals, v)
					if v > math.MaxInt64-step {
						break
					}
				}
			} else {
				for v := start; v >= stop; v += step {
					vals = append(vals, v)
					if v < math.MinInt64-step {
						break
					}
				}
			}
			genResults[k] = seriesResult{colIdx: sc.ColIdx, vals: vals, wrapped: sc.Wrapped}
			if len(vals) > maxLen {
				maxLen = len(vals)
			}
		}

		// Evaluate unnest(array) SRF columns. M0097-0106.
		type unnestResult struct {
			colIdx int
			vals   []Datum
		}
		unnestResults := make([]unnestResult, len(o.plan.UnnestCols))
		for k, uc := range o.plan.UnnestCols {
			arrD, err := evalExpr(uc.ArrExpr, childRow, ctx)
			if err != nil {
				return err
			}
			var vals []Datum
			if !arrD.IsNull() {
				// Use expandArrayDatum for multi-dimensional array flattening (PG semantics).
				elems := expandArrayDatum(arrD)
				for _, d := range elems {
					if !d.IsNull() && uc.CastType != "" {
						// Apply element-level cast when unnest(...)::type syntax used. M0097-0035.
						if cd, cerr := evalCastTyped(d, uc.CastType, "", 0, ctx); cerr == nil {
							d = cd
						}
					}
					vals = append(vals, d)
				}
			}
			unnestResults[k] = unnestResult{colIdx: uc.ColIdx, vals: vals}
			if len(vals) > maxLen {
				maxLen = len(vals)
			}
		}

		// Evaluate regexp_matches(string, pattern[, flags]) SRF columns. Each
		// step's value is one WHOLE match's capture-group array (not one
		// flattened element like unnest), one row per match with the 'g'
		// flag. M0122-0002.
		type regexpMatchesResult struct {
			colIdx int
			vals   []Datum
		}
		regexpMatchesResults := make([]regexpMatchesResult, len(o.plan.RegexpMatchesCols))
		for k, rc := range o.plan.RegexpMatchesCols {
			sD, err := evalExpr(rc.StringExpr, childRow, ctx)
			if err != nil {
				return err
			}
			patD, err := evalExpr(rc.PatternExpr, childRow, ctx)
			if err != nil {
				return err
			}
			var flagsD Datum
			if rc.FlagsExpr != nil {
				flagsD, err = evalExpr(rc.FlagsExpr, childRow, ctx)
				if err != nil {
					return err
				}
			}
			vals, rmErr := evalRegexpMatchesSRF(sD, patD, flagsD)
			if rmErr != nil {
				return rmErr
			}
			regexpMatchesResults[k] = regexpMatchesResult{colIdx: rc.ColIdx, vals: vals}
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
		hasWrappers := len(o.plan.Wrappers) > 0
		for step := 0; step < maxLen; step++ {
			outRow := make(Row, n)
			copy(outRow, otherVals)
			// Eval row for nested-SRF wrappers: child row in [0:ChildWidth),
			// wrapped-SRF per-step raw values in the temp slots above. M0118-0008.
			var evalRow Row
			if hasWrappers {
				evalRow = make(Row, o.plan.EvalRowWidth)
				copy(evalRow, childRow)
			}
			for _, sr := range genResults {
				var val Datum
				if step < len(sr.vals) {
					val = NewIntDatum(sr.vals[step])
				} else {
					val = Datum{} // NULL
				}
				if sr.wrapped {
					evalRow[sr.colIdx] = val
				} else {
					outRow[sr.colIdx] = val
				}
			}
			for _, unnr := range unnestResults {
				if step < len(unnr.vals) {
					outRow[unnr.colIdx] = unnr.vals[step]
				} else {
					outRow[unnr.colIdx] = Datum{} // NULL
				}
			}
			for _, ur := range userResults {
				if step < len(ur.vals) {
					outRow[ur.colIdx] = ur.vals[step]
				} else {
					outRow[ur.colIdx] = Datum{} // NULL
				}
			}
			for _, rmr := range regexpMatchesResults {
				if step < len(rmr.vals) {
					outRow[rmr.colIdx] = rmr.vals[step]
				} else {
					outRow[rmr.colIdx] = Datum{} // NULL
				}
			}
			// Apply nested-SRF wrappers: each reads its SRF's per-step value
			// from evalRow and writes the enclosing scalar result. M0118-0008.
			for _, w := range o.plan.Wrappers {
				d, werr := evalExpr(w.Expr, evalRow, ctx)
				if werr != nil {
					return werr
				}
				outRow[w.OutCol] = d
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
