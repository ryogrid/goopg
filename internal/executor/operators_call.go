package executor

import (
	"fmt"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/plpgsql"
)

// callOp executes `CALL proc(...)` (M0015 Stage B).
type callOp struct {
	plan    *planner.Call
	ctx     *Context
	routine *catalog.Routine
	args    []Datum
	done    bool
}

func newCallOp(p *planner.Call) *callOp {
	return &callOp{plan: p}
}

func (o *callOp) Schema() planner.Schema {
	if o.routine == nil {
		return nil
	}
	schema := make(planner.Schema, 0, len(o.routine.ArgModes))
	for i, mode := range o.routine.ArgModes {
		if mode == "o" || mode == "b" {
			colName := ""
			if i < len(o.routine.ArgNames) {
				colName = o.routine.ArgNames[i]
			}
			colType := catalog.Type{Name: "text"}
			if i < len(o.routine.ArgTypes) {
				colType = o.routine.ArgTypes[i]
			}
			schema = append(schema, planner.SchemaColumn{
				Name: colName,
				Type: colType,
			})
		}
	}
	return schema
}

func (o *callOp) Open(ctx *Context) error {
	o.ctx = ctx

	// Resolve the procedure at Open time so Schema() can report
	// OUT/INOUT columns before Next().
	st := o.plan.Stmt
	rs := routineRegistry(ctx)
	if rs == nil {
		return &ExecError{Code: "42883", Pos: st.Pos(),
			Message: fmt.Sprintf("procedure %s does not exist", st.Name.Name)}
	}

	routines := rs.LookupByName(st.Name)
	if len(routines) == 0 {
		return &ExecError{Code: "42883", Pos: st.Pos(),
			Message: fmt.Sprintf("procedure %s does not exist", st.Name.Name)}
	}

	// Evaluate CALL arguments.
	frame := newPLpgSQLFrame()
	args := make([]Datum, len(st.Args))
	for i, arg := range st.Args {
		d, err := evalPLpgSQLExpr(arg, frame, ctx)
		if err != nil {
			return err
		}
		args[i] = d
	}

	// Match by argument count.
	matches := make([]*catalog.Routine, 0, 1)
	for _, c := range routines {
		if len(c.ArgTypes) == len(args) {
			matches = append(matches, c)
		}
	}
	switch len(matches) {
	case 0:
		return &ExecError{Code: "42883", Pos: st.Pos(),
			Message: fmt.Sprintf("procedure %s does not exist", st.Name.Name)}
	case 1:
		o.routine = matches[0]
	default:
		return &ExecError{Code: "42725", Pos: st.Pos(),
			Message: fmt.Sprintf("procedure %s is ambiguous", st.Name.Name)}
	}

	o.args = args
	return nil
}

func (o *callOp) Close() error { return nil }

func (o *callOp) Next() (Row, error) {
	if o.done {
		return nil, EOF
	}
	o.done = true
	if o.routine == nil {
		return nil, nil
	}

	r := o.routine
	block, err := plpgsql.Parse(r.Body)
	if err != nil {
		return nil, &ExecError{Code: "P0000", Pos: o.plan.Stmt.Pos(),
			Message: fmt.Sprintf("invalid PL/pgSQL body for procedure %s: %v", r.QualifiedName(), err)}
	}

	child := NewContext()
	if o.ctx != nil {
		*child = *o.ctx
	}
	child.Params = make([]Datum, len(o.args))
	frame := newPLpgSQLFrame()

	for i, arg := range o.args {
		declared := catalog.Type{Name: "unknown"}
		if i < len(r.ArgTypes) {
			declared = normalizeCatalogType(r.ArgTypes[i])
		}
		mode := "i"
		if i < len(r.ArgModes) {
			mode = r.ArgModes[i]
		}
		// OUT params get NULL input; INOUT and IN get the caller's value.
		if mode == "o" {
			child.Params[i] = NullDatum
			if i < len(r.ArgNames) && r.ArgNames[i] != "" {
				_ = frame.add(r.ArgNames[i], declared, NullDatum)
			}
			continue
		}
		coerced, err := coerceDatumToType(arg, declared, o.plan.Stmt.Pos(), fmt.Sprintf("argument %d", i+1))
		if err != nil {
			return nil, err
		}
		child.Params[i] = coerced
		if i < len(r.ArgNames) {
			if err := frame.add(r.ArgNames[i], declared, coerced); err != nil {
				return nil, &ExecError{Code: "42P13", Pos: o.plan.Stmt.Pos(), Message: err.Error()}
			}
		}
	}

	for _, d := range block.Declarations {
		typ := catalogTypeFromColumnType(d.Type)
		value := NullDatum
		if d.Default != nil {
			value, err = evalPLpgSQLExpr(d.Default, frame, child)
			if err != nil {
				return nil, err
			}
		}
		value, err = coerceDatumToType(value, typ, d.Pos(), fmt.Sprintf("variable %q", d.Name))
		if err != nil {
			return nil, err
		}
		if err := frame.add(d.Name, typ, value); err != nil {
			return nil, &ExecError{Code: "42P13", Pos: d.Pos(), Message: err.Error()}
		}
	}

	_, _, err = executePLpgSQLStmtList(block.Statements, r, frame, child)
	if err != nil {
		return nil, err
	}

	// Build output row from OUT/INOUT parameters.
	if len(r.ArgModes) == 0 {
		return nil, nil
	}
	outRow := make(Row, 0, len(r.ArgModes))
	for i, mode := range r.ArgModes {
		if mode == "o" || mode == "b" {
			name := ""
			if i < len(r.ArgNames) {
				name = r.ArgNames[i]
			}
			if name != "" {
				if idx, ok := frame.lookup(name); ok {
					outRow = append(outRow, frame.values[idx])
				} else {
					outRow = append(outRow, NullDatum)
				}
			} else {
				outRow = append(outRow, NullDatum)
			}
		}
	}
	return outRow, nil
}
