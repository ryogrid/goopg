package executor

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/plpgsql"
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

	st := o.plan.Stmt

	// Evaluate CALL arguments using PL/pgSQL expression evaluator.
	// Use an empty frame since CALL args are SQL expressions (not
	// PL/pgSQL variable references).
	frame := newPLpgSQLFrame()
	args := make([]Datum, len(st.Args))
	for i, arg := range st.Args {
		d, err := evalPLpgSQLExpr(arg, frame, o.ctx)
		if err != nil {
			return nil, err
		}
		args[i] = d
	}

	// Look up the procedure in the catalog.
	rs := routineRegistry(o.ctx)
	if rs == nil {
		return nil, &ExecError{Code: "42883", Pos: st.Pos(),
			Message: fmt.Sprintf("procedure %s does not exist", st.Name.Name)}
	}

	routines := rs.LookupByName(st.Name)
	matches := make([]*catalog.Routine, 0, 1)
	for _, c := range routines {
		if len(c.ArgTypes) == len(args) {
			matches = append(matches, c)
		}
	}
	var r *catalog.Routine
	switch len(matches) {
	case 0:
		return nil, &ExecError{Code: "42883", Pos: st.Pos(),
			Message: fmt.Sprintf("procedure %s does not exist", st.Name.Name)}
	case 1:
		r = matches[0]
	default:
		return nil, &ExecError{Code: "42725", Pos: st.Pos(),
			Message: fmt.Sprintf("procedure %s is ambiguous", st.Name.Name)}
	}

	if err := o.execProcedure(r, args); err != nil {
		return nil, err
	}
	return nil, nil
}

func (o *callOp) execProcedure(r *catalog.Routine, args []Datum) error {
	lang := strings.ToLower(r.Language)
	switch lang {
	case "plpgsql":
		return o.execPLpgSQLProcedure(r, args)
	case "sql":
		return &ExecError{Code: "0A000", Pos: o.plan.Stmt.Pos(),
			Message: "SQL-language procedures are not yet implemented in v0"}
	}
	return &ExecError{Code: "0A000", Pos: o.plan.Stmt.Pos(),
		Message: fmt.Sprintf("procedure language %q is not executable in v0", lang)}
}

func (o *callOp) execPLpgSQLProcedure(r *catalog.Routine, args []Datum) error {
	block, err := plpgsql.Parse(r.Body)
	if err != nil {
		return &ExecError{Code: "P0000", Pos: o.plan.Stmt.Pos(),
			Message: fmt.Sprintf("invalid PL/pgSQL body for procedure %s: %v", r.QualifiedName(), err)}
	}

	child := NewContext()
	if o.ctx != nil {
		*child = *o.ctx
	}
	child.Params = make([]Datum, len(args))
	frame := newPLpgSQLFrame()

	for i, arg := range args {
		declared := catalog.Type{Name: "unknown"}
		if i < len(r.ArgTypes) {
			declared = normalizeCatalogType(r.ArgTypes[i])
		}
		coerced, err := coerceDatumToType(arg, declared, o.plan.Stmt.Pos(), fmt.Sprintf("argument %d", i+1))
		if err != nil {
			return err
		}
		child.Params[i] = coerced
		if i < len(r.ArgNames) {
			if err := frame.add(r.ArgNames[i], declared, coerced); err != nil {
				return &ExecError{Code: "42P13", Pos: o.plan.Stmt.Pos(), Message: err.Error()}
			}
		}
	}

	for _, d := range block.Declarations {
		typ := catalogTypeFromColumnType(d.Type)
		value := NullDatum
		if d.Default != nil {
			value, err = evalPLpgSQLExpr(d.Default, frame, child)
			if err != nil {
				return err
			}
		}
		value, err = coerceDatumToType(value, typ, d.Pos(), fmt.Sprintf("variable %q", d.Name))
		if err != nil {
			return err
		}
		if err := frame.add(d.Name, typ, value); err != nil {
			return &ExecError{Code: "42P13", Pos: d.Pos(), Message: err.Error()}
		}
	}

	_, _, err = executePLpgSQLStmtList(block.Statements, r, frame, child)
	return err
}
