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
	var schema planner.Schema
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
	// Return nil (not empty slice) when no OUT params — prevents RowDescription
	// from being sent, which causes psql to show "--\n(0 rows)" for IN-only procedures.
	return schema // nil if no OUT params appended
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
		// Build arg-list string like "()" for error message.
		argList := buildArgListStr(len(st.Args))
		// Check if name exists as a function (not procedure) → "is not a procedure" hint.
		fnName := st.Name
		allOverloads := rs.LookupByName(fnName)
		isBuiltinFunc := isKnownBuiltinFunction(strings.ToLower(st.Name.Name))
		if isBuiltinFunc {
			return &ExecError{Code: "42809", Pos: st.Pos(),
				Message: fmt.Sprintf("%s%s is not a procedure", st.Name.Name, argList),
				Hint:    "To call a function, use SELECT."}
		}
		if len(allOverloads) > 0 {
			for _, ov := range allOverloads {
				if !ov.IsProcedure {
					return &ExecError{Code: "42809", Pos: st.Pos(),
						Message: fmt.Sprintf("%s%s is not a procedure", st.Name.Name, argList),
						Hint:    "To call a function, use SELECT."}
				}
			}
		}
		return &ExecError{Code: "42883", Pos: st.Pos(),
			Message:  fmt.Sprintf("procedure %s%s does not exist", st.Name.Name, argList),
			Hint:     "No procedure matches the given name and argument types. You might need to add explicit type casts.",
		}
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
	// Track named-arg names for reordering after routine resolution.
	callerArgNames := st.ArgNames // nil if no named args

	// Match by number of IN arguments. OUT/INOUT params are excluded
	// from the arg count comparison since CALL doesn't require values
	// for them. Caller may provide fewer args than declared (defaults fill).
	inArgCount := len(st.Args)
	matches := make([]*catalog.Routine, 0, 1)
	for _, c := range routines {
		inCount := 0
		for _, mode := range c.ArgModes {
			if mode == "" || mode == "i" || mode == "b" {
				inCount++
			}
		}
		// If no ArgModes, all args are IN (backward compat).
		if c.ArgModes == nil {
			inCount = len(c.ArgTypes)
		}
		// Accept if caller provides at most as many args as declared
		// (defaults fill missing trailing args).
		if inArgCount <= inCount {
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

	// If named args were provided, reorder them to match routine parameter order.
	if len(callerArgNames) > 0 && len(callerArgNames) == len(args) {
		r := o.routine
		reordered := make([]Datum, len(r.ArgTypes))
		// Fill positional slots first (unnamed args), then named args.
		namedByName := map[string]Datum{}
		for i, nm := range callerArgNames {
			if nm == "" {
				reordered[i] = args[i]
			} else {
				namedByName[strings.ToLower(nm)] = args[i]
			}
		}
		// Match named args to parameter positions by name.
		for i, paramName := range r.ArgNames {
			if d, ok := namedByName[strings.ToLower(paramName)]; ok {
				reordered[i] = d
			}
		}
		args = reordered
	}

	o.args = args
	return nil
}

func (o *callOp) Close() error { return nil }

func (o *callOp) Next() (TupleSlot, error) {
	if o.done {
		return nil, EOF
	}
	o.done = true
	if o.routine == nil {
		return nil, nil
	}

	r := o.routine

	// SQL-language procedures: execute each statement with bound params.
	if strings.EqualFold(r.Language, "sql") {
		var callPos int
		if o.plan != nil && o.plan.Stmt != nil {
			callPos = o.plan.Stmt.Pos()
		}
		if err := executeSQLProcedure(r, o.args, o.ctx, callPos); err != nil {
			return nil, err
		}
		// Return a tuple matching the OUT-param schema (empty for IN-only procedures).
		sch := o.Schema()
		if sch == nil || len(sch) == 0 {
			return nil, EOF
		}
		return asSlot(sch, make(Row, len(sch))), nil
	}

	block, err := plpgsql.Parse(r.Body)
	if err != nil {
		return nil, &ExecError{Code: "P0000", Pos: o.plan.Stmt.Pos(),
			Message: fmt.Sprintf("invalid PL/pgSQL body for procedure %s: %v", r.QualifiedName(), err)}
	}

	child := NewContext()
	if o.ctx != nil {
		*child = *o.ctx
	}
	child.Params = make([]Datum, len(r.ArgTypes))
	frame := newPLpgSQLFrame()

	argIdx := 0
	for i := 0; i < len(r.ArgTypes); i++ {
		declared := normalizeCatalogType(r.ArgTypes[i])
		mode := "i"
		if i < len(r.ArgModes) {
			mode = r.ArgModes[i]
		}

		switch mode {
		case "o":
			// OUT param: no caller value, default to NULL.
			child.Params[i] = NullDatum
			if i < len(r.ArgNames) && r.ArgNames[i] != "" {
				_ = frame.add(r.ArgNames[i], declared, NullDatum)
			}
		case "b":
			// INOUT param: caller provides a value.
			if argIdx >= len(o.args) {
				return nil, &ExecError{Code: "42P13", Pos: o.plan.Stmt.Pos(),
					Message: "not enough arguments for procedure"}
			}
			coerced, err := coerceDatumToType(o.args[argIdx], declared, o.plan.Stmt.Pos(), fmt.Sprintf("argument %d", argIdx+1))
			if err != nil {
				return nil, err
			}
			child.Params[i] = coerced
			if i < len(r.ArgNames) {
				if err := frame.add(r.ArgNames[i], declared, coerced); err != nil {
					return nil, &ExecError{Code: "42P13", Pos: o.plan.Stmt.Pos(), Message: err.Error()}
				}
			}
			argIdx++
		default:
			// IN param: caller provides a value.
			if argIdx >= len(o.args) {
				return nil, &ExecError{Code: "42P13", Pos: o.plan.Stmt.Pos(),
					Message: "not enough arguments for procedure"}
			}
			coerced, err := coerceDatumToType(o.args[argIdx], declared, o.plan.Stmt.Pos(), fmt.Sprintf("argument %d", argIdx+1))
			if err != nil {
				return nil, err
			}
			child.Params[i] = coerced
			if i < len(r.ArgNames) {
				if err := frame.add(r.ArgNames[i], declared, coerced); err != nil {
					return nil, &ExecError{Code: "42P13", Pos: o.plan.Stmt.Pos(), Message: err.Error()}
				}
			}
			argIdx++
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
	return asSlot(o.Schema(), outRow), nil
}

// buildArgListStr returns "()" for 0 args or "(unknown,unknown,...)" for n args.
// Matches PostgreSQL's "procedure name(unknown,...) does not exist" format.
func buildArgListStr(n int) string {
	if n == 0 {
		return "()"
	}
	parts := make([]string, n)
	for i := range parts {
		parts[i] = "unknown"
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// isKnownBuiltinFunction returns true if name is a known built-in SQL function
// (not a user-defined routine). Used to detect "random() is not a procedure"
// cases where the function exists in the built-in registry but not in the
// user-routine registry.
func isKnownBuiltinFunction(name string) bool {
	switch name {
	case "random", "now", "current_timestamp", "current_date", "current_time",
		"current_user", "session_user", "current_schema", "current_catalog",
		"abs", "ceil", "floor", "round", "trunc", "sqrt", "exp", "ln", "log",
		"mod", "power", "pow", "sign", "pi", "sin", "cos", "tan", "asin",
		"acos", "atan", "atan2", "setseed", "random_normal",
		"length", "char_length", "upper", "lower", "trim", "btrim", "ltrim",
		"rtrim", "substr", "substring", "concat", "replace", "repeat",
		"position", "strpos", "split_part", "left", "right", "reverse",
		"lpad", "rpad", "initcap", "ascii", "chr", "quote_literal",
		"quote_ident", "format", "translate", "overlay",
		"to_char", "to_date", "to_timestamp", "to_number",
		"coalesce", "nullif", "greatest", "least",
		"count", "sum", "avg", "min", "max",
		"array_length", "array_upper", "array_lower", "cardinality",
		"generate_series", "unnest",
		"pg_sleep", "pg_typeof", "version", "clock_timestamp",
		"date_trunc", "date_part", "extract", "age", "make_date",
		"encode", "decode", "md5", "sha256":
		return true
	}
	return false
}
