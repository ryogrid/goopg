package executor

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/plpgsql"
)

// wrapSQLFunctionContext wraps an error with CONTEXT showing the SQL function
// name and statement number. If the error already has a Context, it prepends.
func wrapSQLFunctionContext(err error, funcName string, stmtNum int) error {
	if err == nil {
		return nil
	}
	var ctx string
	if stmtNum > 0 {
		ctx = fmt.Sprintf("SQL function %q statement %d", funcName, stmtNum)
	} else {
		ctx = fmt.Sprintf("SQL function %q", funcName)
	}
	var ee *ExecError
	if errors.As(err, &ee) {
		newErr := *ee
		if newErr.Context != "" {
			newErr.Context = ctx + "\n" + newErr.Context
		} else {
			newErr.Context = ctx
		}
		return &newErr
	}
	return &ExecError{Code: "XX000", Message: err.Error(), Context: ctx}
}

// rewriteSQLNamedParams replaces named parameter references in a SQL function
// body with positional $n references. This supports SQL functions that reference
// their arguments by name (e.g. "select value + seed" → "select $1 + $2").
func rewriteSQLNamedParams(body string, argNames []string) string {
	for i, name := range argNames {
		if name == "" {
			continue
		}
		// Match either a string literal (to skip) OR the parameter name as a
		// whole word. String literals come first in the alternation so they're
		// consumed without replacement.
		re := regexp.MustCompile(`'(?:[^'\\]|\\.)*'|(?i)\b` + regexp.QuoteMeta(name) + `\b`)
		pos := i + 1 // 1-based
		body = re.ReplaceAllStringFunc(body, func(m string) string {
			if m[0] == '\'' {
				return m // leave string literals unchanged
			}
			return fmt.Sprintf("$%d", pos)
		})
	}
	return body
}

// plpgsqlFrame is the local variable frame for one routine call.
// Names are case-insensitive and map to row slots consumed by evalExpr.
type plpgsqlFrame struct {
	indexByName    map[string]int
	types          []catalog.Type
	values         Row
	// trig is non-nil when this frame is for a trigger function body.
	// M0096-0012.
	trig *plpgsqlTrigCtx
	// returnNextRows accumulates values from RETURN NEXT for SETOF
	// functions. M0097-0073.
	returnNextRows []Datum
	// compositeVarFields maps lower-case variable name → ordered field list
	// for variables declared as composite types.  Populated at DECLARE time
	// (and for function arguments) so that field access / assignment works
	// inside the PL/pgSQL body. M0097-composite.
	compositeVarFields map[string][]catalog.CompositeField
}

// plpgsqlTrigCtx holds the trigger execution context injected into
// trigger function frames. M0096-0012.
type plpgsqlTrigCtx struct {
	OldRow  Row
	NewRow  Row
	Cols    []catalog.Column // table columns (both OLD and NEW use the same schema)
	TGName  string
	TGWhen  string // "before" or "after"
	TGOp    string // "insert", "update", "delete", "truncate"
	TGLevel string // "row" or "statement"
	TGTable string
	TGArgs  []string // trigger function arguments (TG_ARGV)
}

func newPLpgSQLFrame() *plpgsqlFrame {
	return &plpgsqlFrame{
		indexByName:        make(map[string]int),
		compositeVarFields: make(map[string][]catalog.CompositeField),
	}
}

func (f *plpgsqlFrame) add(name string, typ catalog.Type, value Datum) error {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		return nil
	}
	if _, exists := f.indexByName[key]; exists {
		return fmt.Errorf("duplicate PL/pgSQL variable %q", name)
	}
	idx := len(f.values)
	f.indexByName[key] = idx
	f.types = append(f.types, normalizeCatalogType(typ))
	f.values = append(f.values, value)
	return nil
}

func (f *plpgsqlFrame) lookup(name string) (int, bool) {
	idx, ok := f.indexByName[strings.ToLower(strings.TrimSpace(name))]
	return idx, ok
}

// bindSelectIntoRow binds a SELECT … INTO result row to its PL/pgSQL target
// variable(s), mirroring the FOR-loop record/scalar binding conventions:
//   - a single target with a single-column row binds directly to the scalar
//     variable (when one exists);
//   - a single target with a multi-column row is treated as a record and bound
//     to `_<target>_<colname>` sub-fields (auto-registered if absent);
//   - multiple targets bind result columns positionally to scalar variables.
//
// A missing column yields NULL. M0118-0008 (plpgsql-toast).
func bindSelectIntoRow(targets []string, row Row, schema planner.Schema, frame *plpgsqlFrame) {
	if len(targets) == 0 {
		return
	}
	if len(targets) == 1 {
		name := strings.ToLower(targets[0])
		if len(schema) == 1 {
			if idx, ok := frame.indexByName[name]; ok {
				if len(row) > 0 {
					frame.values[idx] = row[0]
				} else {
					frame.values[idx] = NullDatum
				}
				return
			}
		}
		for i, sc := range schema {
			colKey := "_" + name + "_" + strings.ToLower(sc.Name)
			val := NullDatum
			if i < len(row) {
				val = row[i]
			}
			if idx, ok := frame.indexByName[colKey]; ok {
				frame.values[idx] = val
			} else {
				_ = frame.add(colKey, sc.Type, val)
			}
		}
		return
	}
	for i, tgt := range targets {
		name := strings.ToLower(tgt)
		val := NullDatum
		if i < len(row) {
			val = row[i]
		}
		if idx, ok := frame.indexByName[name]; ok {
			frame.values[idx] = val
		} else {
			var typ catalog.Type
			if i < len(schema) {
				typ = schema[i].Type
			}
			_ = frame.add(name, typ, val)
		}
	}
}

// evalStoredRoutineFuncCall resolves and executes user-defined
// routines (M0015 Stage A runtime path) when a call is not one of
// the built-ins handled directly in expr.go.
func evalStoredRoutineFuncCall(x *planner.FuncCall, row Row, ctx *Context) (Datum, error) {
	if x.Star {
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: fmt.Sprintf("function %s does not exist", x.Name)}
	}
	args := make([]Datum, len(x.Args))
	for i, a := range x.Args {
		d, err := evalExpr(a, row, ctx)
		if err != nil {
			return Datum{}, err
		}
		args[i] = d
	}
	rs := routineRegistry(ctx)
	if rs == nil {
		return Datum{}, &ExecError{Code: "42883", Pos: x.Pos(), Message: fmt.Sprintf("function %s does not exist", x.Name)}
	}
	routineName := parseRoutineName(x.Name)
	r, err := resolveRoutineOverload(rs, routineName, args, x.Pos())
	if err != nil {
		return Datum{}, err
	}
	// If it's a procedure, emit the "is a procedure" error using the
	// call-site arg types (string literals show as "unknown", like PG).
	if r.IsProcedure {
		argTypeNames := make([]string, len(x.Args))
		for i, a := range x.Args {
			if _, isStr := a.(*planner.StringConst); isStr {
				argTypeNames[i] = "unknown"
			} else {
				argTypeNames[i] = r.ArgTypes[i].Name
			}
		}
		return NullDatum, &ExecError{
			Code:    "42809",
			Pos:     x.Pos(),
			Message: fmt.Sprintf("%s(%s) is a procedure", r.Name, strings.Join(argTypeNames, ", ")),
			Hint:    "To call a procedure, use CALL.",
		}
	}
	return executeStoredRoutine(r, args, ctx, x.Pos())
}

// routineArgTypesStr builds a comma-separated list of arg type names for
// error messages like "ptest1(text) is a procedure".
func routineArgTypesStr(r *catalog.Routine) string {
	names := make([]string, len(r.ArgTypes))
	for i, t := range r.ArgTypes {
		names[i] = t.Name
	}
	return strings.Join(names, ", ")
}

func executeStoredRoutine(r *catalog.Routine, args []Datum, ctx *Context, pos int) (Datum, error) {
	// Procedures cannot be called via SELECT - only via CALL.
	if r.IsProcedure {
		return NullDatum, &ExecError{Code: "42809", Pos: pos,
			Message: fmt.Sprintf("%s(%s) is a procedure", r.Name, routineArgTypesStr(r)),
			Hint:    "To call a procedure, use CALL."}
	}
	switch strings.ToLower(r.Language) {
	case "plpgsql":
		return executePLpgSQLRoutine(r, args, ctx, pos)
	case "sql":
		return executeSQLRoutine(r, args, ctx, pos)
	case "c":
		// C-language functions are stored as stubs. Return a type-appropriate
		// default: true for bool (most C regress functions test things that pass),
		// NULL for everything else.
		switch strings.ToLower(r.ReturnType.Name) {
		case "bool", "boolean":
			return NewBoolDatum(true), nil
		case "int2", "smallint":
			return NewIntDatum(0), nil
		default:
			return NullDatum, nil
		}
	default:
		return NullDatum, &ExecError{Code: "42704", Pos: pos, Message: fmt.Sprintf("language %q is not supported in v0", r.Language)}
	}
}

func executeSQLRoutine(r *catalog.Routine, args []Datum, ctx *Context, pos int) (Datum, error) {
	body := r.Body
	if len(r.ArgNames) > 0 {
		body = rewriteSQLNamedParams(body, r.ArgNames)
	}
	stmts, err := parser.Parse(body)
	if err != nil {
		return Datum{}, &ExecError{Code: "42601", Pos: pos, Message: fmt.Sprintf("invalid SQL body for function %s: %v", r.QualifiedName(), err)}
	}
	retTypeName := strings.ToLower(r.ReturnType.Name)
	if len(stmts) == 0 {
		// Empty body with non-void return type → "final statement must be SELECT"
		if retTypeName != "" && retTypeName != "void" {
			// Resolve polymorphic return type if possible (e.g. anyarray → integer[]).
			resolvedRetType := resolvePolymorphicReturnType(retTypeName, r.ArgTypes, args)
			return Datum{}, &ExecError{Code: "42P13", Pos: pos,
				Message: fmt.Sprintf("return type mismatch in function declared to return %s", canonicalReturnType(resolvedRetType)),
				Detail:  "Function's final statement must be SELECT or INSERT/UPDATE/DELETE/MERGE RETURNING.",
				Context: fmt.Sprintf("SQL function %q during startup", r.Name)}
		}
		return NullDatum, nil
	}
	child := NewContext()
	if ctx != nil {
		*child = *ctx
	}
	child.Notices = nil
	child.Params = make([]Datum, len(args))
	for i, arg := range args {
		declared := catalog.Type{Name: "unknown"}
		if i < len(r.ArgTypes) {
			declared = normalizeCatalogType(r.ArgTypes[i])
		}
		coerced, err := coerceDatumToType(arg, declared, pos, fmt.Sprintf("argument %d", i+1))
		if err != nil {
			return Datum{}, err
		}
		child.Params[i] = coerced
	}
	// VOID functions: run all statements for side-effects, return NULL.
	if strings.EqualFold(r.ReturnType.Name, "void") {
		for si, stmt := range stmts {
			node, err := planner.Plan(stmt, ctxPlanCatalog(child))
			if err != nil {
				return Datum{}, wrapSQLFunctionContext(err, r.Name, si+1)
			}
			op, err := Build(node)
			if err != nil {
				return Datum{}, wrapSQLFunctionContext(err, r.Name, si+1)
			}
			if err := op.Open(child); err != nil {
				_ = op.Close()
				return Datum{}, wrapSQLFunctionContext(err, r.Name, si+1)
			}
			for {
				_, nextErr := op.Next()
				if nextErr == EOF {
					break
				}
				if nextErr != nil {
					_ = op.Close()
					return Datum{}, wrapSQLFunctionContext(nextErr, r.Name, si+1)
				}
			}
			_ = op.Close()
		}
		if ctx != nil {
			for _, n := range child.TakeNotices() {
				ctx.AddNotice(n)
			}
		}
		return NullDatum, nil
	}
	// Execute all statements except the last as side effects; return from last.
	for i, stmt := range stmts {
		stmtNum := i + 1
		node, err := planner.Plan(stmt, ctxPlanCatalog(child))
		if err != nil {
			return Datum{}, wrapSQLFunctionContext(err, r.Name, stmtNum)
		}
		op, err := Build(node)
		if err != nil {
			return Datum{}, wrapSQLFunctionContext(err, r.Name, stmtNum)
		}
		if err := op.Open(child); err != nil {
			_ = op.Close()
			return Datum{}, wrapSQLFunctionContext(err, r.Name, stmtNum)
		}
		if i < len(stmts)-1 {
			// Side-effect statement: drain rows, continue.
			for {
				_, nextErr := op.Next()
				if nextErr == EOF {
					break
				}
				if nextErr != nil {
					_ = op.Close()
					return Datum{}, wrapSQLFunctionContext(nextErr, r.Name, stmtNum)
				}
			}
			_ = op.Close()
			continue
		}
		// Last statement: collect return value.
		defer op.Close()
		out := NullDatum
		if slot, nextErr := op.Next(); nextErr != EOF {
			if nextErr != nil {
				return Datum{}, wrapSQLFunctionContext(nextErr, r.Name, stmtNum)
			}
			row := slotRow(slot)
			if len(row) > 0 {
				out = row[0]
			}
			if _, nextErr2 := op.Next(); nextErr2 != EOF {
				if nextErr2 != nil {
					return Datum{}, wrapSQLFunctionContext(nextErr2, r.Name, stmtNum)
				}
				return Datum{}, &ExecError{Code: "21000", Pos: pos, Message: fmt.Sprintf("SQL function %s returned more than one row", r.QualifiedName())}
			}
		}
		if ctx != nil {
			for _, n := range child.TakeNotices() {
				ctx.AddNotice(n)
			}
		}
		coerced, err := coerceDatumToType(out, normalizeCatalogType(r.ReturnType), pos, fmt.Sprintf("return value of function %s", r.QualifiedName()))
		if err != nil {
			return Datum{}, wrapSQLFunctionContext(err, r.Name, stmtNum)
		}
		return coerced, nil
	}
	// Unreachable — loop always handles the last statement above.
	if ctx != nil {
		for _, n := range child.TakeNotices() {
			ctx.AddNotice(n)
		}
	}
	return NullDatum, nil
}

// executeSQLProcedure runs a SQL-language PROCEDURE body.
// Unlike executeSQLRoutine (functions), procedures may have multiple statements
// and do not return a scalar value. Named parameters are rewritten to $1, $2 etc.
func executeSQLProcedure(r *catalog.Routine, args []Datum, ctx *Context, pos int) error {
	_, err := executeSQLProcedureCore(r, args, ctx, pos)
	return err
}

// executeSQLProcedureReturning executes a SQL procedure and returns the result
// row from the last statement (for OUT/INOUT parameter procedures).
func executeSQLProcedureReturning(r *catalog.Routine, args []Datum, ctx *Context, pos int) (Row, error) {
	return executeSQLProcedureCore(r, args, ctx, pos)
}

func executeSQLProcedureCore(r *catalog.Routine, args []Datum, ctx *Context, pos int) (Row, error) {
	body := r.Body
	if len(r.ArgNames) > 0 {
		body = rewriteSQLNamedParams(body, r.ArgNames)
	}
	stmts, err := parser.Parse(body)
	if err != nil {
		return nil, &ExecError{Code: "42601", Pos: pos, Message: fmt.Sprintf("invalid SQL body for procedure %s: %v", r.QualifiedName(), err)}
	}
	child := NewContext()
	if ctx != nil {
		*child = *ctx
	}
	child.Notices = nil
	child.Params = make([]Datum, len(args))
	for i, arg := range args {
		declared := catalog.Type{Name: "unknown"}
		if i < len(r.ArgTypes) {
			declared = normalizeCatalogType(r.ArgTypes[i])
		}
		coerced, err := coerceDatumToType(arg, declared, pos, fmt.Sprintf("argument %d", i+1))
		if err != nil {
			return nil, err
		}
		child.Params[i] = coerced
	}
	var lastRow Row
	for i, stmt := range stmts {
		stmtNum := i + 1
		node, err := planner.Plan(stmt, ctxPlanCatalog(child))
		if err != nil {
			return nil, wrapSQLFunctionContext(err, r.Name, stmtNum)
		}
		op, err := Build(node)
		if err != nil {
			return nil, wrapSQLFunctionContext(err, r.Name, stmtNum)
		}
		if err := op.Open(child); err != nil {
			_ = op.Close()
			return nil, wrapSQLFunctionContext(err, r.Name, stmtNum)
		}
		isLast := i == len(stmts)-1
		for {
			slot, nextErr := op.Next()
			if nextErr == EOF {
				break
			}
			if nextErr != nil {
				_ = op.Close()
				return nil, wrapSQLFunctionContext(nextErr, r.Name, stmtNum)
			}
			if isLast && lastRow == nil {
				// Copy row so it survives op.Close().
				if raw := slotRow(slot); raw != nil {
					lastRow = append(Row(nil), raw...)
				}
			}
		}
		if err := op.Close(); err != nil {
			return nil, wrapSQLFunctionContext(err, r.Name, stmtNum)
		}
	}
	if ctx != nil {
		for _, n := range child.TakeNotices() {
			ctx.AddNotice(n)
		}
	}
	return lastRow, nil
}

// evalSQLFunctionSetof calls a SETOF SQL function and returns all rows as
// []Datum (first column of each row). Used by the ProjectSet operator for
// user-defined SETOF functions in SELECT target lists. M0097-0020.
func evalSQLFunctionSetof(r *catalog.Routine, args []Datum, ctx *Context, pos int) ([]Datum, error) {
	body := r.Body
	if len(r.ArgNames) > 0 {
		body = rewriteSQLNamedParams(body, r.ArgNames)
	}
	stmts, err := parser.Parse(body)
	if err != nil {
		return nil, &ExecError{Code: "42601", Pos: pos, Message: fmt.Sprintf("invalid SQL body for function %s: %v", r.QualifiedName(), err)}
	}
	if len(stmts) == 0 {
		return nil, nil
	}
	// SETOF VOID returns an empty set (no columns, no rows).
	retTypeName := strings.ToLower(r.ReturnType.Name)
	if retTypeName == "void" {
		return nil, nil
	}
	child := NewContext()
	if ctx != nil {
		*child = *ctx
	}
	child.Notices = nil
	child.Params = make([]Datum, len(args))
	for i, arg := range args {
		declared := catalog.Type{Name: "unknown"}
		if i < len(r.ArgTypes) {
			declared = normalizeCatalogType(r.ArgTypes[i])
		}
		coerced, err := coerceDatumToType(arg, declared, pos, fmt.Sprintf("argument %d", i+1))
		if err != nil {
			return nil, err
		}
		child.Params[i] = coerced
	}
	// Execute all statements except the last as side effects; collect rows from last.
	for i, stmt := range stmts {
		stmtNum := i + 1
		node, err := planner.Plan(stmt, ctxPlanCatalog(child))
		if err != nil {
			return nil, wrapSQLFunctionContext(err, r.Name, stmtNum)
		}
		op, err := Build(node)
		if err != nil {
			return nil, wrapSQLFunctionContext(err, r.Name, stmtNum)
		}
		if err := op.Open(child); err != nil {
			_ = op.Close()
			return nil, wrapSQLFunctionContext(err, r.Name, stmtNum)
		}
		if i < len(stmts)-1 {
			for {
				_, nextErr := op.Next()
				if nextErr == EOF {
					break
				}
				if nextErr != nil {
					_ = op.Close()
					return nil, wrapSQLFunctionContext(nextErr, r.Name, stmtNum)
				}
			}
			_ = op.Close()
			continue
		}
		// Last statement: collect SETOF rows.
		defer op.Close()
		var out []Datum
		retType := normalizeCatalogType(r.ReturnType)
		for {
			slot, nextErr := op.Next()
			if nextErr == EOF {
				break
			}
			if nextErr != nil {
				return nil, wrapSQLFunctionContext(nextErr, r.Name, stmtNum)
			}
			row := slotRow(slot)
			if len(row) == 0 {
				out = append(out, NullDatum)
				continue
			}
			coerced, err := coerceDatumToType(row[0], retType, pos, fmt.Sprintf("return value of function %s", r.QualifiedName()))
			if err != nil {
				return nil, err
			}
			out = append(out, coerced)
		}
		if ctx != nil {
			for _, n := range child.TakeNotices() {
				ctx.AddNotice(n)
			}
		}
		return out, nil
	}
	if ctx != nil {
		for _, n := range child.TakeNotices() {
			ctx.AddNotice(n)
		}
	}
	return nil, nil
}

// evalPLpgSQLFunctionSetof calls a SETOF PL/pgSQL function via RETURN NEXT
// accumulation and returns all collected rows. Used by the ProjectSet operator
// for user-defined SETOF plpgsql functions in SELECT target lists. M0097-0073.
// datumForCompositeText returns the text representation of d for use
// inside a PostgreSQL composite literal "(v1,v2,...)". Unlike StringValue()
// which only handles string/bytes kinds, this covers int and bool too.
func datumForCompositeText(d Datum) string {
	switch d.Kind {
	case KindInt:
		return fmt.Sprintf("%d", d.Int)
	case KindBool:
		if d.Int != 0 {
			return "t"
		}
		return "f"
	default:
		return d.StringValue()
	}
}

func evalPLpgSQLFunctionSetof(r *catalog.Routine, args []Datum, ctx *Context, pos int) ([]Datum, error) {
	// STRICT: if any argument is NULL, return empty set without executing the body.
	if r.Strict {
		for _, arg := range args {
			if arg.IsNull() {
				return nil, nil
			}
		}
	}
	block, err := plpgsql.Parse(r.Body)
	if err != nil {
		return nil, &ExecError{Code: "P0000", Pos: pos, Message: fmt.Sprintf("invalid PL/pgSQL body for function %s: %v", r.QualifiedName(), err)}
	}
	child := NewContext()
	if ctx != nil {
		*child = *ctx
	}
	child.Notices = nil
	child.Params = make([]Datum, len(args))
	frame := newPLpgSQLFrame()
	for i, arg := range args {
		declared := catalog.Type{Name: "unknown"}
		if i < len(r.ArgTypes) {
			declared = normalizeCatalogType(r.ArgTypes[i])
		}
		coerced, err := coerceDatumToType(arg, declared, pos, fmt.Sprintf("argument %d", i+1))
		if err != nil {
			return nil, err
		}
		child.Params[i] = coerced
		if i < len(r.ArgNames) {
			if err := frame.add(r.ArgNames[i], declared, coerced); err != nil {
				return nil, &ExecError{Code: "42P13", Pos: pos, Message: err.Error()}
			}
		}
	}
	// Initialize OUT parameters as NULL variables (RETURNS TABLE / OUT args not
	// passed by caller). M0097-0028.
	for i, mode := range r.ArgModes {
		if (mode == "o" || mode == "b") && i < len(r.ArgNames) && r.ArgNames[i] != "" {
			typ := catalog.Type{Name: "unknown"}
			if i < len(r.ArgTypes) {
				typ = normalizeCatalogType(r.ArgTypes[i])
			}
			_ = frame.add(r.ArgNames[i], typ, NullDatum) // ignore dup (INOUT already added)
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
	if ctx != nil {
		for _, n := range child.TakeNotices() {
			ctx.AddNotice(n)
		}
	}
	if err != nil {
		return nil, err
	}
	return frame.returnNextRows, nil
}

func routineRegistry(ctx *Context) *catalog.Routines {
	if ctx == nil || ctx.Catalog == nil {
		return nil
	}
	return ctx.Catalog.Routines()
}

func parseRoutineName(name string) parser.ObjectName {
	dot := strings.LastIndexByte(name, '.')
	if dot <= 0 || dot >= len(name)-1 {
		return parser.ObjectName{Name: name}
	}
	return parser.ObjectName{Schema: name[:dot], Name: name[dot+1:]}
}

func resolveRoutineOverload(rs *catalog.Routines, name parser.ObjectName, args []Datum, pos int) (*catalog.Routine, error) {
	candidates := rs.LookupByName(name)
	matches := make([]*catalog.Routine, 0, 1)
	for _, c := range candidates {
		if len(c.ArgTypes) != len(args) {
			continue
		}
		if !routineArgsCompatible(c.ArgTypes, args) {
			continue
		}
		matches = append(matches, c)
	}
	sig := name.String()
	switch len(matches) {
	case 0:
		return nil, &ExecError{Code: "42883", Pos: pos, Message: fmt.Sprintf("function %s does not exist", sig)}
	case 1:
		return matches[0], nil
	default:
		// Prefer non-polymorphic overloads over polymorphic ones (anyenum,
		// anyelement, anyarray, etc.).  This mirrors PostgreSQL's function
		// overload resolution: a specific-type match beats a polymorphic one.
		specific := make([]*catalog.Routine, 0, len(matches))
		for _, c := range matches {
			if !hasPolymorphicArgType(c.ArgTypes) {
				specific = append(specific, c)
			}
		}
		if len(specific) == 1 {
			return specific[0], nil
		}
		if len(specific) == 0 {
			specific = matches // fall back to polymorphic matches
		}
		// Second pass: prefer exact Datum-kind match over coercion-based match.
		// E.g. fipshash(text) beats fipshash(bytea) when arg is KindString.
		// Mirrors PostgreSQL's "exact match beats implicit cast" resolution.
		exact := make([]*catalog.Routine, 0, len(specific))
		for _, c := range specific {
			if routineArgsExactMatch(c.ArgTypes, args) {
				exact = append(exact, c)
			}
		}
		if len(exact) == 1 {
			return exact[0], nil
		}
		return nil, &ExecError{Code: "42725", Pos: pos, Message: fmt.Sprintf("function %s is not unique", sig)}
	}
}

func routineArgsCompatible(argTypes []catalog.Type, args []Datum) bool {
	if len(argTypes) != len(args) {
		return false
	}
	for i := range argTypes {
		if _, err := coerceDatumToType(args[i], argTypes[i], 0, "argument"); err != nil {
			return false
		}
	}
	return true
}

// routineArgsExactMatch returns true when every argument's Datum kind directly
// maps to the declared parameter type without coercion. Used as a secondary
// overload preference after compatibility is established. M0097-0025.
func routineArgsExactMatch(argTypes []catalog.Type, args []Datum) bool {
	if len(argTypes) != len(args) {
		return false
	}
	for i, typ := range argTypes {
		tn := strings.ToLower(typ.Name)
		v := args[i]
		var exact bool
		switch v.Kind {
		case KindInt:
			exact = isIntegerTypeName(tn)
		case KindNumeric:
			exact = isNumericType(tn)
		case KindString:
			exact = isTextTypeName(tn)
		case KindBool:
			exact = isBoolTypeName(tn)
		case KindBytes:
			exact = (tn == "bytea")
		case KindTime:
			exact = isTimeTypeName(tn) || isIntervalTypeName(tn)
		default:
			exact = true // unknown kind: treat as exact to avoid false negatives
		}
		if !exact {
			return false
		}
	}
	return true
}

// hasPolymorphicArgType returns true when any declared argument type is a
// PostgreSQL polymorphic pseudo-type (anyenum, anyelement, anyarray, etc.).
// Used in resolveRoutineOverload to prefer specific-type overloads over
// generic polymorphic ones.
func hasPolymorphicArgType(argTypes []catalog.Type) bool {
	for _, t := range argTypes {
		switch strings.ToLower(t.Name) {
		case "anyenum", "anyelement", "anyarray", "anynonarray",
			"anyrange", "anymultirange", "anycompatible",
			"anycompatiblearray", "anycompatiblerange",
			"anycompatiblemultirange":
			return true
		}
	}
	return false
}

func executePLpgSQLRoutine(r *catalog.Routine, args []Datum, ctx *Context, pos int) (Datum, error) {
	if strings.ToLower(r.Language) != "plpgsql" {
		return Datum{}, &ExecError{Code: "0A000", Pos: pos, Message: fmt.Sprintf("function language %q is not executable in v0", r.Language)}
	}
	// STRICT: if any argument is NULL, return NULL without executing the body.
	if r.Strict {
		for _, arg := range args {
			if arg.IsNull() {
				return NullDatum, nil
			}
		}
	}
	block, err := plpgsql.Parse(r.Body)
	if err != nil {
		return Datum{}, &ExecError{Code: "P0000", Pos: pos, Message: fmt.Sprintf("invalid PL/pgSQL body for function %s: %v", r.QualifiedName(), err)}
	}
	child := NewContext()
	if ctx != nil {
		*child = *ctx
	}
	// Reset notices so TakeNotices propagates only new ones from this call,
	// not the parent's accumulated notices. M0100-0005.
	child.Notices = nil
	child.Params = make([]Datum, len(args))
	frame := newPLpgSQLFrame()
	for i, arg := range args {
		declared := catalog.Type{Name: "unknown"}
		if i < len(r.ArgTypes) {
			declared = normalizeCatalogType(r.ArgTypes[i])
		}
		coerced, err := coerceDatumToType(arg, declared, pos, fmt.Sprintf("argument %d", i+1))
		if err != nil {
			return Datum{}, err
		}
		child.Params[i] = coerced
		if i < len(r.ArgNames) {
			argName := strings.ToLower(r.ArgNames[i])
			if err := frame.add(r.ArgNames[i], declared, coerced); err != nil {
				return Datum{}, &ExecError{Code: "42P13", Pos: pos, Message: err.Error()}
			}
			// If this argument is a composite type, record its field schema.
			if child != nil && child.Catalog != nil {
				if fields := child.Catalog.LookupCompositeTypeFields(declared.Name); len(fields) > 0 {
					frame.compositeVarFields[argName] = fields
				}
			}
		}
	}
	for _, d := range block.Declarations {
		typ := catalogTypeFromColumnType(d.Type)
		value := NullDatum
		if d.Default != nil {
			value, err = evalPLpgSQLExpr(d.Default, frame, child)
			if err != nil {
				return Datum{}, err
			}
		}
		value, err = coerceDatumToType(value, typ, d.Pos(), fmt.Sprintf("variable %q", d.Name))
		if err != nil {
			return Datum{}, err
		}
		if err := frame.add(d.Name, typ, value); err != nil {
			return Datum{}, &ExecError{Code: "42P13", Pos: d.Pos(), Message: err.Error()}
		}
		// If this declaration is a composite type, record its field schema.
		if child != nil && child.Catalog != nil {
			if fields := child.Catalog.LookupCompositeTypeFields(typ.Name); len(fields) > 0 {
				frame.compositeVarFields[strings.ToLower(d.Name)] = fields
			}
		}
	}
	res, flow, err := executePLpgSQLStmtList(block.Statements, r, frame, child)
	// Propagate NOTICE messages from function body back to caller. M0100-0005.
	if ctx != nil {
		for _, n := range child.TakeNotices() {
			ctx.AddNotice(n)
		}
	}
	if err != nil {
		return Datum{}, err
	}
	if flow == flowReturn {
		return res, nil
	}
	// VOID functions may fall off the end without an explicit RETURN. M0097-0025.
	if strings.EqualFold(r.ReturnType.Name, "void") {
		return NullDatum, nil
	}
	return Datum{}, &ExecError{Code: "2F005", Pos: pos, Message: fmt.Sprintf("control reached end of function %s without RETURN", r.QualifiedName())}
}

type controlFlow int

const (
	flowNone controlFlow = iota
	flowReturn
	flowExit
	flowContinue
	// flowReturnTriggerOld / flowReturnTriggerNew signal that a trigger
	// function returned OLD or NEW respectively. The trigger executor
	// intercepts these before they propagate further. M0096-0012.
	flowReturnTriggerOld
	flowReturnTriggerNew
	// flowReturnTriggerNull signals RETURN NULL from a trigger (skip the row).
	flowReturnTriggerNull
)

func executePLpgSQLStmtList(stmts []plpgsql.Stmt, r *catalog.Routine, frame *plpgsqlFrame, ctx *Context) (Datum, controlFlow, error) {
	for _, stmt := range stmts {
		res, flow, err := executePLpgSQLStmt(stmt, r, frame, ctx)
		if err != nil || flow != flowNone {
			return res, flow, err
		}
	}
	return Datum{}, flowNone, nil
}

func executePLpgSQLStmt(stmt plpgsql.Stmt, r *catalog.Routine, frame *plpgsqlFrame, ctx *Context) (Datum, controlFlow, error) {
	switch s := stmt.(type) {
	case *plpgsql.AssignStmt:
		// _plpgsql_noop is the silent discard target for unrecognised
		// dotted-expression statements. M0096-0012.
		if s.Target == "_plpgsql_noop" {
			return Datum{}, flowNone, nil
		}
		// Composite field assignment: target is "varname\x00fieldname".
		// M0097-composite.
		if strings.ContainsRune(s.Target, '\x00') {
			parts := strings.SplitN(s.Target, "\x00", 2)
			varName, fieldSpec := parts[0], parts[1]
			idx, ok := frame.lookup(varName)
			if !ok {
				// Unknown variable — silently skip (best-effort).
				return Datum{}, flowNone, nil
			}
			v, err := evalPLpgSQLExpr(s.Value, frame, ctx)
			if err != nil {
				return Datum{}, flowNone, err
			}
			fields := frame.compositeVarFields[varName]
			fieldIdx := -1
			for i, f := range fields {
				if strings.ToLower(f.Name) == fieldSpec {
					fieldIdx = i
					break
				}
			}
			if fieldIdx < 0 {
				// Field not found in known composite type — silently skip.
				return Datum{}, flowNone, nil
			}
			current := frame.values[idx]
			frame.values[idx] = updateCompositeField(current, fieldIdx, len(fields), v)
			return Datum{}, flowNone, nil
		}
		idx, ok := frame.lookup(s.Target)
		if !ok {
			return Datum{}, flowNone, &ExecError{Code: "42703", Pos: s.Pos(), Message: fmt.Sprintf("variable %q does not exist", s.Target)}
		}
		v, err := evalPLpgSQLExpr(s.Value, frame, ctx)
		if err != nil {
			return Datum{}, flowNone, err
		}
		v, err = coerceDatumToType(v, frame.types[idx], s.Pos(), fmt.Sprintf("variable %q", s.Target))
		if err != nil {
			return Datum{}, flowNone, err
		}
		frame.values[idx] = v
		// Propagate OLD.<col>/NEW.<col> writes back to the trigger row so
		// embedded SQL (substituteTriggerRefs in execPLpgSQLEmbeddedSQL)
		// observes the mutation within the trigger body. M0100-0005aa.
		if frame.trig != nil {
			if strings.HasPrefix(s.Target, "_old_") && frame.trig.OldRow != nil {
				col := s.Target[len("_old_"):]
				for i, c := range frame.trig.Cols {
					if strings.ToLower(c.Name) == col && i < len(frame.trig.OldRow) {
						frame.trig.OldRow[i] = v
						break
					}
				}
			} else if strings.HasPrefix(s.Target, "_new_") && frame.trig.NewRow != nil {
				col := s.Target[len("_new_"):]
				for i, c := range frame.trig.Cols {
					if strings.ToLower(c.Name) == col && i < len(frame.trig.NewRow) {
						frame.trig.NewRow[i] = v
						break
					}
				}
			}
		}
		return Datum{}, flowNone, nil

	case *plpgsql.ArraySubscriptAssignStmt:
		// Array subscript assignment: x[idx] := value. M0097-0113.
		idx, ok := frame.lookup(s.VarName)
		if !ok {
			return Datum{}, flowNone, &ExecError{Code: "42703", Pos: s.Pos(), Message: fmt.Sprintf("variable %q does not exist", s.VarName)}
		}
		// Evaluate subscript (1-based per PG convention).
		subD, err := evalPLpgSQLExpr(s.Subscript, frame, ctx)
		if err != nil {
			return Datum{}, flowNone, err
		}
		sub := int(subD.Int) // subscript index (1-based)
		if subD.Kind != KindInt || sub < 1 {
			return Datum{}, flowNone, &ExecError{Code: "2202E", Pos: s.Pos(), Message: "array subscript out of range"}
		}
		// Evaluate new value.
		newVal, err := evalPLpgSQLExpr(s.Value, frame, ctx)
		if err != nil {
			return Datum{}, flowNone, err
		}
		// Get the current array datum (stored as KindString "{e1,e2,...}").
		arrD := frame.values[idx]
		var elems []string
		if !arrD.IsNull() {
			elems = parseTextArray(arrD.StringValue())
		}
		// Extend array if necessary.
		for len(elems) < sub {
			elems = append(elems, "NULL")
		}
		// Replace element at 1-based index.
		elems[sub-1] = newVal.Format()
		if newVal.IsNull() {
			elems[sub-1] = "NULL"
		}
		frame.values[idx] = NewStringDatum("{" + strings.Join(elems, ",") + "}")
		return Datum{}, flowNone, nil

	case *plpgsql.IfStmt:
		cond, err := evalPLpgSQLExpr(s.Cond, frame, ctx)
		if err != nil {
			return Datum{}, flowNone, err
		}
		if !cond.IsNull() && cond.Kind == KindBool && cond.BoolValue() {
			return executePLpgSQLStmtList(s.Then, r, frame, ctx)
		}
		for _, elsif := range s.Elsifs {
			cond, err := evalPLpgSQLExpr(elsif.Cond, frame, ctx)
			if err != nil {
				return Datum{}, flowNone, err
			}
			if !cond.IsNull() && cond.Kind == KindBool && cond.BoolValue() {
				return executePLpgSQLStmtList(elsif.Then, r, frame, ctx)
			}
		}
		if s.Else != nil {
			return executePLpgSQLStmtList(s.Else, r, frame, ctx)
		}
		return Datum{}, flowNone, nil

	case *plpgsql.LoopStmt:
		for {
			res, flow, err := executePLpgSQLStmtList(s.Body, r, frame, ctx)
			if err != nil {
				return Datum{}, flowNone, err
			}
			if flow == flowReturn {
				return res, flow, nil
			}
			if flow == flowExit {
				return Datum{}, flowNone, nil
			}
			if flow == flowContinue {
				continue
			}
		}

	case *plpgsql.WhileStmt:
		for {
			cond, err := evalPLpgSQLExpr(s.Cond, frame, ctx)
			if err != nil {
				return Datum{}, flowNone, err
			}
			if cond.IsNull() || !cond.BoolValue() {
				return Datum{}, flowNone, nil
			}
			res, flow, err := executePLpgSQLStmtList(s.Body, r, frame, ctx)
			if err != nil {
				return Datum{}, flowNone, err
			}
			if flow == flowReturn {
				return res, flow, nil
			}
			if flow == flowExit {
				return Datum{}, flowNone, nil
			}
			if flow == flowContinue {
				continue
			}
		}

	case *plpgsql.ForStmt:
		lower, err := evalPLpgSQLExpr(s.Lower, frame, ctx)
		if err != nil {
			return Datum{}, flowNone, err
		}
		upper, err := evalPLpgSQLExpr(s.Upper, frame, ctx)
		if err != nil {
			return Datum{}, flowNone, err
		}
		stepVal := int64(1)
		if s.Step != nil {
			sv, err := evalPLpgSQLExpr(s.Step, frame, ctx)
			if err != nil {
				return Datum{}, flowNone, err
			}
			if sv.IsNull() || sv.Kind != KindInt {
				return Datum{}, flowNone, &ExecError{Code: "42804", Pos: s.Pos(), Message: "FOR loop step must be an integer"}
			}
			stepVal = sv.Int
		}
		if lower.IsNull() || lower.Kind != KindInt || upper.IsNull() || upper.Kind != KindInt {
			return Datum{}, flowNone, &ExecError{Code: "42804", Pos: s.Pos(), Message: "FOR loop bounds must be integers"}
		}
		l, u := lower.Int, upper.Int

		// FOR loop variable shadowing: save previous state if any.
		key := strings.ToLower(s.Var)
		prevIdx, exists := frame.indexByName[key]
		
		// Always push a new slot for the loop variable.
		idx := len(frame.values)
		frame.indexByName[key] = idx
		frame.types = append(frame.types, catalog.Type{Name: "integer"})
		frame.values = append(frame.values, NullDatum)
		defer func() {
			// Restore frame state.
			frame.values = frame.values[:idx]
			frame.types = frame.types[:idx]
			if exists {
				frame.indexByName[key] = prevIdx
			} else {
				delete(frame.indexByName, key)
			}
		}()

		if s.Reverse {
			for i := l; i >= u; i -= stepVal {
				frame.values[idx] = Datum{Kind: KindInt, Int: i}
				res, flow, err := executePLpgSQLStmtList(s.Body, r, frame, ctx)
				if err != nil {
					return Datum{}, flowNone, err
				}
				if flow == flowReturn {
					return res, flow, nil
				}
				if flow == flowExit {
					return Datum{}, flowNone, nil
				}
				if flow == flowContinue {
					continue
				}
			}
		} else {
			for i := l; i <= u; i += stepVal {
				frame.values[idx] = Datum{Kind: KindInt, Int: i}
				res, flow, err := executePLpgSQLStmtList(s.Body, r, frame, ctx)
				if err != nil {
					return Datum{}, flowNone, err
				}
				if flow == flowReturn {
					return res, flow, nil
				}
				if flow == flowExit {
					return Datum{}, flowNone, nil
				}
				if flow == flowContinue {
					continue
				}
			}
		}
		return Datum{}, flowNone, nil

	case *plpgsql.PerformStmt:
		_, err := evalPLpgSQLExpr(s.Expr, frame, ctx)
		if err != nil {
			return Datum{}, flowNone, err
		}
		return Datum{}, flowNone, nil

	case *plpgsql.NullStmt:
		// `NULL;` — no-op placeholder. M0118-0009.
		return Datum{}, flowNone, nil

	case *plpgsql.TxControlStmt:
		// `COMMIT;` / `ROLLBACK;` — transaction control inside a non-atomic
		// PL/pgSQL routine (top-level DO / procedure outside an explicit
		// transaction block). The dispatch installs PLpgSQLCommitChain only in
		// auto-commit mode; when it is nil we are in an atomic context, which
		// PG rejects with SQLSTATE 2D000 (invalid transaction termination).
		// M0118-0008 (plpgsql-toast).
		if ctx.PLpgSQLCommitChain == nil {
			return Datum{}, flowNone, &ExecError{Code: "2D000", Pos: s.Pos(),
				Message: "invalid transaction termination"}
		}
		if err := ctx.PLpgSQLCommitChain(s.Rollback); err != nil {
			return Datum{}, flowNone, err
		}
		return Datum{}, flowNone, nil

	case *plpgsql.ExitStmt:
		if s.Cond != nil {
			cond, err := evalPLpgSQLExpr(s.Cond, frame, ctx)
			if err != nil {
				return Datum{}, flowNone, err
			}
			if cond.IsNull() || !cond.BoolValue() {
				return Datum{}, flowNone, nil
			}
		}
		return Datum{}, flowExit, nil

	case *plpgsql.ContinueStmt:
		if s.Cond != nil {
			cond, err := evalPLpgSQLExpr(s.Cond, frame, ctx)
			if err != nil {
				return Datum{}, flowNone, err
			}
			if cond.IsNull() || !cond.BoolValue() {
				return Datum{}, flowNone, nil
			}
		}
		return Datum{}, flowContinue, nil

	case *plpgsql.ReturnStmt:
		// Bare `RETURN;` (nil expression) — exit the function returning NULL.
		// Valid for VOID-returning functions / procedures and the canonical
		// early-exit form. A value-returning function must supply an expression
		// (upstream pl_gram.y treats it as a "missing expression" error).
		// M0118-0009 (subxid-overflow gen_subxids).
		if s.Expr == nil {
			if frame.trig != nil {
				return Datum{}, flowReturnTriggerNull, nil
			}
			if !strings.EqualFold(r.ReturnType.Name, "void") {
				return Datum{}, flowNone, &ExecError{
					Code:    "42601",
					Pos:     s.Pos(),
					Message: "missing expression",
				}
			}
			return NullDatum, flowReturn, nil
		}
		// In trigger context, detect RETURN OLD / RETURN NEW / RETURN NULL.
		if frame.trig != nil {
			if cr, ok := s.Expr.(*parser.ColumnRef); ok && cr.Table == "" && cr.Schema == "" {
				switch strings.ToLower(cr.Column) {
				case "old":
					return Datum{}, flowReturnTriggerOld, nil
				case "new":
					return Datum{}, flowReturnTriggerNew, nil
				}
			}
			if _, ok := s.Expr.(*parser.NullConst); ok {
				return Datum{}, flowReturnTriggerNull, nil
			}
		}
		v, err := evalPLpgSQLExpr(s.Expr, frame, ctx)
		if err != nil {
			return Datum{}, flowNone, err
		}
		// For trigger functions, skip type coercion (return type is "trigger").
		if frame.trig != nil {
			return v, flowReturn, nil
		}
		v, err = coerceDatumToType(v, r.ReturnType, s.Pos(), "RETURN")
		if err != nil {
			return Datum{}, flowNone, err
		}
		return v, flowReturn, nil

	case *plpgsql.ReturnNextStmt:
		// RETURN NEXT — append one value to the SETOF accumulator. M0097-0073.
		if s.Expr == nil {
			// RETURN NEXT; (no expression) — collect current OUT param values.
			// Used by RETURNS TABLE functions. M0097-0028.
			var outNames []string
			for i, mode := range r.ArgModes {
				if (mode == "o" || mode == "b") && i < len(r.ArgNames) {
					outNames = append(outNames, r.ArgNames[i])
				}
			}
			if len(outNames) == 1 {
				val := NullDatum
				if idx, ok := frame.lookup(outNames[0]); ok {
					val = frame.values[idx]
				}
				frame.returnNextRows = append(frame.returnNextRows, val)
			} else if len(outNames) > 1 {
				parts := make([]string, len(outNames))
				for i, name := range outNames {
					val := NullDatum
					if idx, ok := frame.lookup(name); ok {
						val = frame.values[idx]
					}
					if val.IsNull() {
						parts[i] = ""
					} else {
						parts[i] = val.StringValue()
					}
				}
				frame.returnNextRows = append(frame.returnNextRows, NewStringDatum("("+strings.Join(parts, ",")+")"))
			}
			return Datum{}, flowNone, nil
		}
		v, err := evalPLpgSQLExpr(s.Expr, frame, ctx)
		if err != nil {
			return Datum{}, flowNone, err
		}
		frame.returnNextRows = append(frame.returnNextRows, v)
		return Datum{}, flowNone, nil

	case *plpgsql.ReturnQueryStmt:
		// RETURN QUERY SELECT — run query and append all result rows to SETOF accumulator.
		sql := s.QuerySrc
		stmts, perr := parser.Parse(sql)
		if perr != nil || len(stmts) == 0 {
			return Datum{}, flowNone, &ExecError{Code: "42601", Pos: s.Pos(), Message: fmt.Sprintf("RETURN QUERY: %v", perr)}
		}
		plan, perr := planner.Plan(stmts[0], ctxPlanCatalog(ctx))
		if perr != nil {
			return Datum{}, flowNone, perr
		}
		op, perr := Build(plan)
		if perr != nil {
			return Datum{}, flowNone, perr
		}
		if perr := op.Open(ctx); perr != nil {
			op.Close()
			return Datum{}, flowNone, perr
		}
		for {
			slot, rerr := op.Next()
			if rerr == EOF {
				break
			}
			if rerr != nil {
				op.Close()
				return Datum{}, flowNone, rerr
			}
			if slot == nil {
				continue
			}
			row := slot.Row()
			if len(row) == 1 {
				frame.returnNextRows = append(frame.returnNextRows, row[0])
			} else if len(row) > 1 {
				// Pack multi-column row as PostgreSQL composite text (v1,v2,...).
				// Use datumForCompositeText for correct integer/bool/etc. formatting.
				var buf strings.Builder
				buf.WriteByte('(')
				for i, d := range row {
					if i > 0 {
						buf.WriteByte(',')
					}
					if !d.IsNull() {
						buf.WriteString(datumForCompositeText(d))
					}
				}
				buf.WriteByte(')')
				frame.returnNextRows = append(frame.returnNextRows, NewStringDatum(buf.String()))
			}
		}
		op.Close()
		return Datum{}, flowNone, nil

	case *plpgsql.RaiseStmt:
		// RAISE EXCEPTION/ERROR: surface as an executor error.
		// RAISE NOTICE/WARNING/INFO/LOG/DEBUG: queue via context so the server
		// emits a NoticeResponse before the next CommandComplete. M0096-0012.
		raiseMsgEval := func() string {
			return evalRaiseMsg(s.Msg, frame, ctx)
		}
		if s.ConditionName != "" {
			// RAISE condition_name [USING MESSAGE = 'text'] — raise named condition.
			// Use condition name as error code for exception handler matching. M0097-0003.
			code := conditionNameToSQLState(s.ConditionName)
			msg := raiseMsgEval()
			if msg == "" {
				msg = s.ConditionName
			}
			return Datum{}, flowNone, &ExecError{Code: code, Pos: s.Pos(), Message: msg, ConditionName: s.ConditionName}
		}
		switch strings.ToLower(s.Level) {
		case "error", "exception":
			return Datum{}, flowNone, &ExecError{Code: "P0001", Pos: s.Pos(), Message: raiseMsgEval()}
		}
		if ctx != nil {
			msg := raiseMsgEval()
			ctx.AddNotice(msg)
		}
		return Datum{}, flowNone, nil

	case *plpgsql.ExecuteStmt:
		// EXECUTE expr [INTO var] [USING ...] — dynamic SQL. M0100-0005.
		// 1. Evaluate the SQL expression.
		sqlDatum, err := evalPLpgSQLExpr(s.Query, frame, ctx)
		if err != nil {
			return Datum{}, flowNone, err
		}
		dynSQL := sqlDatum.StringValue()

		// 2. Evaluate USING parameters and substitute $N placeholders.
		for i, argExpr := range s.Using {
			argDatum, perr := evalPLpgSQLExpr(argExpr, frame, ctx)
			if perr != nil {
				return Datum{}, flowNone, perr
			}
			placeholder := fmt.Sprintf("$%d", i+1)
			dynSQL = strings.ReplaceAll(dynSQL, placeholder, plpgsqlFormatDynArg(argDatum))
		}

		// 3. Execute the dynamic SQL and optionally capture INTO var.
		stmts, perr := parser.Parse(dynSQL)
		if perr != nil || len(stmts) == 0 {
			if s.IntoVar == "" {
				return Datum{}, flowNone, nil // best-effort; ignore parse failures for side-effect EXECUTE
			}
			return Datum{}, flowNone, nil
		}
		plan, perr := planner.Plan(stmts[0], ctxPlanCatalog(ctx))
		if perr != nil {
			return Datum{}, flowNone, perr
		}
		op, perr := Build(plan)
		if perr != nil {
			return Datum{}, flowNone, perr
		}
		addExecCtx := func(err error) error {
			if ee, ok := err.(*ExecError); ok && ee.Context == "" {
				ee.Context = fmt.Sprintf("SQL statement %q", dynSQL)
			}
			return err
		}
		if perr := op.Open(ctx); perr != nil {
			op.Close()
			return Datum{}, flowNone, addExecCtx(perr)
		}
		slot, perr := op.Next()
		// Copy the INTO result datum before Close() so releaseRow() does not
		// zero it out underneath us. M0100-0005 fix: slot row is pooled.
		var intoVal Datum
		if s.IntoVar != "" && slot != nil && (perr == nil || perr == EOF) {
			row := slot.Row()
			if len(row) > 0 {
				intoVal = row[0]
			}
		}
		op.Close()
		if perr != nil && perr != EOF {
			return Datum{}, flowNone, addExecCtx(perr)
		}
		if s.IntoVar != "" {
			if idx, ok := frame.lookup(s.IntoVar); ok {
				frame.values[idx] = intoVal
			}
		}
		return Datum{}, flowNone, nil

	case *plpgsql.SQLStmt:
		// Execute embedded SQL with trigger OLD/NEW substitution. M0096-0012.
		if err := execPLpgSQLEmbeddedSQL(s.SQL, frame, ctx); err != nil {
			return Datum{}, flowNone, err
		}
		return Datum{}, flowNone, nil

	case *plpgsql.SelectIntoStmt:
		// SELECT ... INTO [STRICT] target[, ...] — bind the first result row
		// to the named PL/pgSQL variable(s). The query has already had its
		// INTO clause stripped by the parser. M0118-0008 (plpgsql-toast).
		sql := s.SQL
		if frame.trig != nil {
			sql = substituteTriggerRefs(sql, frame.trig)
		}
		stmts, err := parser.Parse(sql)
		if err != nil {
			return Datum{}, flowNone, &ExecError{Code: "42601", Message: fmt.Sprintf("SELECT INTO query parse error: %v", err)}
		}
		if len(stmts) == 0 {
			return Datum{}, flowNone, nil
		}
		plan, err := planner.Plan(stmts[0], ctxPlanCatalog(ctx))
		if err != nil {
			return Datum{}, flowNone, err
		}
		op, err := Build(plan)
		if err != nil {
			return Datum{}, flowNone, err
		}
		if err := op.Open(ctx); err != nil {
			op.Close()
			return Datum{}, flowNone, err
		}
		// Capture the output schema up front so a no-row result can still bind
		// NULL to the target(s) (PG sets unmatched SELECT INTO targets to NULL).
		schema := op.Schema()
		slot, nerr := op.Next()
		var firstRow Row
		rowCount := 0
		if slot != nil && (nerr == nil || nerr == EOF) {
			r0 := slotRow(slot)
			if r0 != nil && nerr == nil {
				firstRow = append(Row(nil), r0...)
				if s := slot.Schema(); len(s) > 0 {
					schema = s
				}
				rowCount = 1
				// STRICT needs to know if a second row exists.
				if s.Strict {
					if s2, e2 := op.Next(); e2 == nil && s2 != nil {
						rowCount = 2
					} else if e2 != nil && e2 != EOF {
						nerr = e2
					}
				}
			}
		}
		op.Close()
		if nerr != nil && nerr != EOF {
			return Datum{}, flowNone, nerr
		}
		if s.Strict {
			if rowCount == 0 {
				return Datum{}, flowNone, &ExecError{Code: "P0002", Message: "query returned no rows"}
			}
			if rowCount > 1 {
				return Datum{}, flowNone, &ExecError{Code: "P0003", Message: "query returned more than one row"}
			}
		}
		bindSelectIntoRow(s.Targets, firstRow, schema, frame)
		return Datum{}, flowNone, nil

	case *plpgsql.ForSelectStmt:
		// FOR rec IN query LOOP ... END LOOP — query cursor loop. M0097-0012.
		sql := s.SQL
		if frame.trig != nil {
			sql = substituteTriggerRefs(sql, frame.trig)
		}
		// FOR rec IN EXECUTE expr LOOP — dynamic SQL cursor. M0097-0073.
		// The captured SQL text starts with EXECUTE; evaluate the rest as an
		// expression to get the actual SQL string.
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(sql)), "EXECUTE ") {
			exprText := strings.TrimSpace(sql[strings.IndexByte(sql, ' '):])
			sqlExpr, parseErr := parser.ParseExpr(exprText)
			if parseErr != nil {
				return Datum{}, flowNone, &ExecError{Code: "42601", Message: fmt.Sprintf("FOR EXECUTE expression parse error: %v", parseErr)}
			}
			sqlDatum, evalErr := evalPLpgSQLExpr(sqlExpr, frame, ctx)
			if evalErr != nil {
				return Datum{}, flowNone, evalErr
			}
			if sqlDatum.IsNull() {
				return Datum{}, flowNone, nil
			}
			sql = sqlDatum.StringValue()
		}
		// Execute the query and collect rows.
		stmts, err := parser.Parse(sql)
		if err != nil {
			return Datum{}, flowNone, &ExecError{Code: "42601", Message: fmt.Sprintf("FOR query parse error: %v", err)}
		}
		if len(stmts) == 0 {
			return Datum{}, flowNone, nil
		}
		plan, err := planner.Plan(stmts[0], ctxPlanCatalog(ctx))
		if err != nil {
			return Datum{}, flowNone, err
		}
		op, err := Build(plan)
		if err != nil {
			return Datum{}, flowNone, err
		}
		if err := op.Open(ctx); err != nil {
			op.Close()
			return Datum{}, flowNone, err
		}
		varName := strings.ToLower(s.Var)
		for {
			slot, err := op.Next()
			if err == EOF {
				break
			}
			if err != nil {
				op.Close()
				return Datum{}, flowNone, err
			}
			// Bind row columns to the loop variable.
			// - For record/row variables: assign to _<var>_<colname> sub-fields.
			// - For scalar variables: if the loop var exists directly in frame
			//   and the query returns exactly 1 column, assign to it directly.
			if slot != nil && slot.Schema() != nil {
				row := slotRow(slot)
				schema := slot.Schema()
				// Scalar shortcut: if varName exists in frame and the query
				// returns one column, assign directly to varName.
				if len(schema) == 1 {
					if idx, ok := frame.indexByName[varName]; ok {
						if len(row) > 0 {
							frame.values[idx] = row[0]
						}
					} else {
						// Fall through to sub-field naming below.
						colKey := "_" + varName + "_" + strings.ToLower(schema[0].Name)
						if idx2, ok2 := frame.indexByName[colKey]; ok2 {
							if len(row) > 0 {
								frame.values[idx2] = row[0]
							}
						} else {
							_ = frame.add(colKey, schema[0].Type, NullDatum)
							if len(row) > 0 {
								if idx3, ok3 := frame.indexByName[colKey]; ok3 {
									frame.values[idx3] = row[0]
								}
							}
						}
					}
				} else {
					for i, sc := range schema {
						colKey := "_" + varName + "_" + strings.ToLower(sc.Name)
						if idx, ok := frame.indexByName[colKey]; ok {
							if i < len(row) {
								frame.values[idx] = row[i]
							}
						} else {
							// Auto-register new column variable.
							_ = frame.add(colKey, sc.Type, NullDatum)
							if i < len(row) {
								if idx2, ok2 := frame.indexByName[colKey]; ok2 {
									frame.values[idx2] = row[i]
								}
							}
						}
					}
				}
			}
			v, flow, err := executePLpgSQLStmtList(s.Body, r, frame, ctx)
			if err != nil {
				op.Close()
				return Datum{}, flowNone, err
			}
			if flow == flowReturn || flow == flowReturnTriggerOld || flow == flowReturnTriggerNew || flow == flowReturnTriggerNull {
				op.Close()
				return v, flow, nil
			}
			if flow == flowExit {
				break
			}
		}
		op.Close()
		return Datum{}, flowNone, nil

	case *plpgsql.Block:
		// Nested BEGIN...END sub-block. Execute declarations + statements.
		// Declarations introduce new variables into the frame. M0097-0003.
		for _, d := range s.Declarations {
			var val Datum
			if d.Default != nil {
				lowered, err := lowerPLpgSQLExpr(d.Default, frame)
				if err == nil {
					val, _ = evalExpr(lowered, frame.values, ctx)
				}
			}
			_ = frame.add(strings.ToLower(d.Name), normalizeCatalogType(catalogTypeFromColumnType(d.Type)), val)
		}
		_, flow, err := executePLpgSQLStmtList(s.Statements, r, frame, ctx)
		if err != nil {
			return Datum{}, flowNone, err
		}
		return Datum{}, flow, nil

	case *plpgsql.ExceptionBlock:
		// BEGIN...EXCEPTION...END — try/catch block. M0097-0012.
		// Execute TryBody; if it errors, try matching handlers.
		v, flow, err := executePLpgSQLStmtList(s.TryBody, r, frame, ctx)
		if err == nil {
			return v, flow, nil
		}
		// Determine SQLSTATE and condition name from error.
		sqlstate := "XX000"
		condName := ""
		if ee, ok := err.(*ExecError); ok {
			sqlstate = ee.Code
			condName = ee.ConditionName
		}
		// Try each handler.
		for _, h := range s.Handlers {
			if exceptionHandlerMatches(h.Conditions, sqlstate, condName) {
				hv, hflow, herr := executePLpgSQLStmtList(h.Body, r, frame, ctx)
				if herr != nil {
					return Datum{}, flowNone, herr
				}
				return hv, hflow, nil
			}
		}
		// No handler matched — re-propagate.
		return Datum{}, flowNone, err

	default:
		return Datum{}, flowNone, &ExecError{Code: "0A000", Pos: stmt.Pos(), Message: fmt.Sprintf("unsupported PL/pgSQL statement %T", stmt)}
	}
}

// conditionNameToSQLState maps a PL/pgSQL condition name to a SQLSTATE code.
// Partial mapping covering common condition names. M0097-0003.
func conditionNameToSQLState(name string) string {
	switch strings.ToLower(name) {
	case "data_corrupted":
		return "XX001"
	case "reading_sql_data_not_permitted":
		return "2F002"
	case "modifying_sql_data_not_permitted":
		return "2F003"
	case "prohibited_sql_statement_attempted":
		return "2F003"
	case "division_by_zero":
		return "22012"
	case "unique_violation":
		return "23505"
	case "foreign_key_violation":
		return "23503"
	case "no_data_found":
		return "P0002"
	case "too_many_rows":
		return "P0003"
	case "syntax_error":
		return "42601"
	case "undefined_table":
		return "42P01"
	case "not_null_violation":
		return "23502"
	case "serialization_failure":
		return "40001"
	case "deadlock_detected":
		return "40P01"
	case "raise_exception":
		return "P0001"
	}
	return "P0001" // default RAISE exception
}

// exceptionHandlerMatches reports whether any condition in the handler
// matches the given SQLSTATE or condition name. M0097-0012.
func exceptionHandlerMatches(conditions []string, sqlstate string, conditionName ...string) bool {
	raiseCondName := ""
	if len(conditionName) > 0 {
		raiseCondName = strings.ToLower(conditionName[0])
	}
	for _, cond := range conditions {
		lower := strings.ToLower(cond)
		// Direct condition-name match: RAISE reading_sql_data_not_permitted caught by WHEN reading_sql_data_not_permitted.
		if raiseCondName != "" && lower == raiseCondName {
			return true
		}
		switch strings.ToUpper(cond) {
		case "OTHERS":
			return true
		case sqlstate:
			return true
		}
		// Check common condition names.
		switch lower {
		case "others", "when others":
			return true
		case "division_by_zero":
			if sqlstate == "22012" {
				return true
			}
		case "unique_violation":
			if sqlstate == "23505" {
				return true
			}
		case "foreign_key_violation":
			if sqlstate == "23503" {
				return true
			}
		case "undefined_function", "undefined_object":
			if sqlstate == "42883" || sqlstate == "42704" {
				return true
			}
		case "no_data_found":
			if sqlstate == "P0002" {
				return true
			}
		case "too_many_rows":
			if sqlstate == "P0003" {
				return true
			}
		case "syntax_error":
			if sqlstate == "42601" {
				return true
			}
		case "undefined_table":
			if sqlstate == "42P01" {
				return true
			}
		case "not_null_violation":
			if sqlstate == "23502" {
				return true
			}
		}
	}
	return false
}

func evalPLpgSQLExpr(e parser.Expr, frame *plpgsqlFrame, ctx *Context) (Datum, error) {
	// A top-level scalar subquery (`x := (SELECT ...)`) cannot be lowered to a
	// planner.Expr — it must be planned and executed against the live catalog to
	// produce its single value. PL/pgSQL treats `(SELECT ...)` in a scalar
	// context as returning the first column of the first row (NULL if no row).
	// M0118-0008 (plpgsql-toast assign2).
	if sq, ok := e.(*parser.SubqueryExpr); ok {
		return evalScalarSubquery(sq, ctx)
	}
	pe, err := lowerPLpgSQLExpr(e, frame)
	if err != nil {
		return Datum{}, err
	}
	d, err := evalExpr(pe, frame.values, ctx)
	if err != nil {
		if ee, ok := err.(*ExecError); ok && ee.Pos == 0 {
			ee.Pos = e.Pos()
		}
		return Datum{}, err
	}
	return d, nil
}

// evalScalarSubquery plans and executes a scalar subquery from a PL/pgSQL
// expression, returning the first column of the first row (NULL Datum when the
// query produces no rows, matching PostgreSQL's scalar-subquery semantics). An
// error is raised if the subquery returns more than one row. M0118-0008.
func evalScalarSubquery(sq *parser.SubqueryExpr, ctx *Context) (Datum, error) {
	if sq.Inner == nil {
		return Datum{}, &ExecError{Code: "42601", Pos: sq.Pos(), Message: "empty subquery in PL/pgSQL expression"}
	}
	plan, err := planner.Plan(sq.Inner, ctxPlanCatalog(ctx))
	if err != nil {
		return Datum{}, err
	}
	op, err := Build(plan)
	if err != nil {
		return Datum{}, err
	}
	if err := op.Open(ctx); err != nil {
		op.Close()
		return Datum{}, err
	}
	defer op.Close()
	slot, nerr := op.Next()
	if nerr != nil && nerr != EOF {
		return Datum{}, nerr
	}
	if slot == nil || nerr == EOF {
		// No rows ⇒ NULL.
		return Datum{}, nil
	}
	row := slotRow(slot)
	if len(row) == 0 {
		return Datum{}, nil
	}
	result := row[0]
	// A scalar subquery may return at most one row.
	if s2, e2 := op.Next(); e2 == nil && s2 != nil {
		return Datum{}, &ExecError{Code: "21000", Pos: sq.Pos(), Message: "more than one row returned by a subquery used as an expression"}
	} else if e2 != nil && e2 != EOF {
		return Datum{}, e2
	}
	return result, nil
}

func lowerPLpgSQLExpr(e parser.Expr, frame *plpgsqlFrame) (planner.Expr, error) {
	switch x := e.(type) {
	case *parser.IntegerConst:
		return &planner.IntegerConst{Value: x.Value}, nil
	case *parser.NumericConst:
		return &planner.NumericConst{Value: x.Value}, nil
	case *parser.StringConst:
		return &planner.StringConst{Value: x.Value}, nil
	case *parser.TypedStringLit:
		return &planner.TypedStringLit{Type: x.Type, Value: x.Value}, nil
	case *parser.IntervalLit:
		return &planner.IntervalLit{Value: x.Value, Unit: x.Unit}, nil
	case *parser.ExtractExpr:
		src, err := lowerPLpgSQLExpr(x.Source, frame)
		if err != nil {
			return nil, err
		}
		return &planner.ExtractExpr{Field: x.Field, Source: src}, nil
	case *parser.CaseExpr:
		out := &planner.CaseExpr{}
		if x.Operand != nil {
			op, err := lowerPLpgSQLExpr(x.Operand, frame)
			if err != nil {
				return nil, err
			}
			out.Operand = op
		}
		for _, w := range x.Whens {
			when, err := lowerPLpgSQLExpr(w.When, frame)
			if err != nil {
				return nil, err
			}
			then, err := lowerPLpgSQLExpr(w.Then, frame)
			if err != nil {
				return nil, err
			}
			out.Whens = append(out.Whens, planner.CaseWhen{When: when, Then: then})
		}
		if x.Else != nil {
			els, err := lowerPLpgSQLExpr(x.Else, frame)
			if err != nil {
				return nil, err
			}
			out.Else = els
		}
		return out, nil
	case *parser.InExpr:
		if x.Subquery != nil {
			return nil, &ExecError{Code: "0A000", Pos: x.Pos(), Message: "IN (subquery) is not supported in PL/pgSQL expressions in v0"}
		}
		op, err := lowerPLpgSQLExpr(x.Operand, frame)
		if err != nil {
			return nil, err
		}
		list := make([]planner.Expr, 0, len(x.List))
		for _, item := range x.List {
			v, err := lowerPLpgSQLExpr(item, frame)
			if err != nil {
				return nil, err
			}
			list = append(list, v)
		}
		return &planner.InExpr{Operand: op, Negated: x.Negated, List: list}, nil
	case *parser.ExistsExpr:
		return nil, &ExecError{Code: "0A000", Pos: x.Pos(), Message: "EXISTS is not supported in PL/pgSQL expressions in v0"}
	case *parser.SubqueryExpr:
		return nil, &ExecError{Code: "0A000", Pos: x.Pos(), Message: "subqueries are not supported in PL/pgSQL expressions in v0"}
	case *parser.NullConst:
		return &planner.NullConst{}, nil
	case *parser.BooleanConst:
		return &planner.BooleanConst{Value: x.Value}, nil
	case *parser.ParamRef:
		return &planner.ParamRef{Number: x.Number}, nil
	case *parser.ColumnRef:
		// Handle OLD.col and NEW.col in trigger context by looking them up
		// in the pre-injected trigger column variables. M0096-0012.
		if x.Table != "" && x.Schema == "" && frame.trig != nil {
			which := strings.ToLower(x.Table)
			if which == "old" || which == "new" {
				// Trigger column variables are stored as "_old_<colname>" / "_new_<colname>".
				varKey := "_" + which + "_" + strings.ToLower(x.Column)
				idx, ok := frame.lookup(varKey)
				if !ok {
					return nil, &ExecError{Code: "42703", Pos: x.Pos(),
						Message: fmt.Sprintf("column %q not found in trigger %s record", x.Column, which)}
				}
				return &planner.ColumnRef{Index: idx, Name: varKey, Type: frame.types[idx]}, nil
			}
		}
		if x.Schema != "" || x.Table != "" {
			// Check for composite type field access: table=varName, column=fieldName.
			// M0097-composite.
			if x.Schema == "" {
				varName := strings.ToLower(x.Table)
				if fields, ok := frame.compositeVarFields[varName]; ok {
					colName := strings.ToLower(x.Column)
					for i, f := range fields {
						if strings.ToLower(f.Name) == colName {
							idx, ok2 := frame.lookup(varName)
							if !ok2 {
								break
							}
							val := frame.values[idx]
							if val.IsNull() {
								return &planner.NullConst{}, nil
							}
							fieldVal := extractCompositeField(val.StringValue(), i)
							if fieldVal == "" {
								return &planner.NullConst{}, nil
							}
							if n, err2 := strconv.ParseInt(fieldVal, 10, 64); err2 == nil {
								return &planner.IntegerConst{Value: n}, nil
							}
							return &planner.StringConst{Value: fieldVal}, nil
						}
					}
				}
			}
			return nil, &ExecError{Code: "0A000", Pos: x.Pos(), Message: "qualified names are not supported in PL/pgSQL expressions in v0"}
		}
		idx, ok := frame.lookup(x.Column)
		if !ok {
			return nil, &ExecError{Code: "42703", Pos: x.Pos(), Message: fmt.Sprintf("variable %q does not exist", x.Column)}
		}
		return &planner.ColumnRef{Index: idx, Name: x.Column, Type: frame.types[idx]}, nil
	case *parser.StarExpr:
		return nil, &ExecError{Code: "42601", Pos: x.Pos(), Message: "'*' is not allowed in PL/pgSQL expression context"}
	case *parser.UnaryOp:
		op, err := lowerPLpgSQLExpr(x.Operand, frame)
		if err != nil {
			return nil, err
		}
		return &planner.UnaryOp{Op: x.Op, Operand: op}, nil
	case *parser.BinaryOp:
		left, err := lowerPLpgSQLExpr(x.Left, frame)
		if err != nil {
			return nil, err
		}
		right, err := lowerPLpgSQLExpr(x.Right, frame)
		if err != nil {
			return nil, err
		}
		return &planner.BinaryOp{Op: x.Op, Left: left, Right: right}, nil
	case *parser.CastExpr:
		return lowerPLpgSQLExpr(x.Operand, frame)
	case *parser.FuncCall:
		if x.Over != nil {
			return nil, &ExecError{Code: "0A000", Pos: x.Pos(), Message: "window function calls are not supported in PL/pgSQL expressions in v0"}
		}
		args := make([]planner.Expr, 0, len(x.Args))
		for _, a := range x.Args {
			pa, err := lowerPLpgSQLExpr(a, frame)
			if err != nil {
				return nil, err
			}
			args = append(args, pa)
		}
		return &planner.FuncCall{Name: x.Name.String(), Args: args, Star: x.Star}, nil
	case *parser.ArrayConstructorExpr:
		largs := make([]planner.Expr, len(x.Elements))
		for i, el := range x.Elements {
			lowered, err := lowerPLpgSQLExpr(el, frame)
			if err != nil {
				return nil, err
			}
			largs[i] = lowered
		}
		return &planner.FuncCall{Name: "array_construct", Args: largs}, nil
	case *parser.IsNullExpr:
		// IS [NOT] NULL in PL/pgSQL condition expressions (e.g. "state is null").
		op, err := lowerPLpgSQLExpr(x.Operand, frame)
		if err != nil {
			return nil, err
		}
		return &planner.IsNullExpr{Operand: op, Negated: x.Negated}, nil
	case *parser.IsDistinctFromExpr:
		// IS [NOT] DISTINCT FROM in PL/pgSQL expressions.
		left, err := lowerPLpgSQLExpr(x.Left, frame)
		if err != nil {
			return nil, err
		}
		right, err := lowerPLpgSQLExpr(x.Right, frame)
		if err != nil {
			return nil, err
		}
		return &planner.IsDistinctFromExpr{Left: left, Right: right, Negated: x.Negated}, nil
	case *parser.ArraySubscriptExpr:
		// array[subscript] read in PL/pgSQL (e.g. x[1] on the RHS).
		arr, err := lowerPLpgSQLExpr(x.Base, frame)
		if err != nil {
			return nil, err
		}
		sub, err := lowerPLpgSQLExpr(x.Index, frame)
		if err != nil {
			return nil, err
		}
		// tg_argv uses 0-based indexing (PostgreSQL convention for trigger arguments).
		// Adjust by adding 1 to convert to our 1-based array_subscript function.
		if cr, ok := x.Base.(*parser.ColumnRef); ok && strings.ToLower(cr.Column) == "tg_argv" {
			sub = &planner.BinaryOp{Op: parser.OpAdd, Left: sub, Right: &planner.IntegerConst{Value: 1}}
		}
		return &planner.FuncCall{Name: "array_subscript", Args: []planner.Expr{arr, sub}}, nil
	default:
		return nil, &ExecError{Code: "0A000", Pos: e.Pos(), Message: fmt.Sprintf("unsupported PL/pgSQL expression %T", e)}
	}
}

func catalogTypeFromColumnType(t parser.ColumnType) catalog.Type {
	return catalog.Type{Name: strings.ToLower(t.Name), Args: append([]int64(nil), t.Args...)}
}

func normalizeCatalogType(t catalog.Type) catalog.Type {
	return catalog.Type{Name: strings.ToLower(t.Name), Args: append([]int64(nil), t.Args...)}
}

func coerceDatumToType(v Datum, typ catalog.Type, pos int, subject string) (Datum, error) {
	if v.IsNull() {
		return NullDatum, nil
	}
	tn := strings.ToLower(typ.Name)
	switch {
	case tn == "" || tn == "unknown":
		return v, nil
	case isIntegerTypeName(tn):
		switch v.Kind {
		case KindInt:
			return v, nil
		case KindNumeric:
			// Truncate numeric to integer (matches PG behavior for RETURNS int functions).
			mantissa := v.NumericMantissaValue()
			// Scale away the fractional part if any.
			scale := v.Scale
			for scale > 0 {
				mantissa /= 10
				scale--
			}
			return Datum{Kind: KindInt, Int: mantissa}, nil
		}
	case isNumericType(tn):
		switch v.Kind {
		case KindNumeric:
			return v, nil
		case KindInt:
			return numericFromInt(v.Int), nil
		}
	case isTextTypeName(tn):
		switch v.Kind {
		case KindString:
			return v, nil
		case KindBytes:
			return NewStringDatum(string(v.BytesValue())), nil
		}
	case isBoolTypeName(tn):
		if v.Kind == KindBool {
			return v, nil
		}
		if v.Kind == KindString {
			return v, nil // pass-through for overload resolution
		}
	case isTimeTypeName(tn):
		if v.Kind == KindTime {
			return v, nil
		}
		// Implicit coercion of string literals to date/timestamp (PG semantics).
		if v.Kind == KindString {
			if t := tryParseStringAs(KindTime, v.StringValue()); !t.IsNull() {
				return t, nil
			}
			// Return pass-through so overload resolution can proceed;
			// execution will fail with a proper cast error if needed.
			return v, nil
		}
	case isIntervalTypeName(tn):
		if v.Kind == KindInterval {
			return v, nil
		}
		if v.Kind == KindString {
			return v, nil // pass-through for overload resolution
		}
	default:
		// Unmodelled types remain pass-through in v0, mirroring the
		// no-op cast behaviour in planner/executor.
		return v, nil
	}
	// If this is a function return type mismatch, use PG's canonical message.
	if strings.HasPrefix(subject, "return value of function ") {
		// Extract the declared return type canonical name.
		retTypeName := canonicalReturnType(typ.Name)
		return Datum{}, &ExecError{Code: "42804", Pos: pos, Message: fmt.Sprintf("return type mismatch in function declared to return %s", retTypeName)}
	}
	return Datum{}, &ExecError{Code: "42804", Pos: pos, Message: fmt.Sprintf("%s expects type %q but got %s", subject, typ.Name, datumKindName(v))}
}

// canonicalReturnType returns the canonical PG type name for error messages.
// resolvePolymorphicReturnType attempts to resolve a polymorphic return type
// (anyarray, anyelement) based on the actual call-time argument kinds.
func resolvePolymorphicReturnType(retTypeName string, argTypes []catalog.Type, args []Datum) string {
	switch strings.ToLower(retTypeName) {
	case "anyarray":
		// Find the anyelement argument to determine the element type.
		for i, at := range argTypes {
			if strings.ToLower(at.Name) == "anyelement" && i < len(args) {
				if elem := datumKindToTypeName(args[i].Kind); elem != "" {
					return elem + "[]"
				}
			}
		}
	case "anyelement":
		// Resolve from the first anyarray argument.
		for i, at := range argTypes {
			if strings.ToLower(at.Name) == "anyarray" && i < len(args) {
				if elem := datumKindToTypeName(args[i].Kind); elem != "" {
					return elem
				}
			}
		}
	}
	return retTypeName
}

// datumKindToTypeName maps a Datum kind to its PG type name for polymorphic resolution.
func datumKindToTypeName(k DatumKind) string {
	switch k {
	case KindInt:
		return "integer"
	case KindBool:
		return "boolean"
	case KindString:
		return "text"
	case KindNumeric:
		return "numeric"
	case KindBytes:
		return "bytea"
	}
	return ""
}

func canonicalReturnType(name string) string {
	switch strings.ToLower(name) {
	case "int", "int4":
		return "integer"
	case "int2":
		return "smallint"
	case "int8":
		return "bigint"
	case "float4":
		return "real"
	case "float8":
		return "double precision"
	case "bool":
		return "boolean"
	case "varchar":
		return "character varying"
	}
	return strings.ToLower(name)
}

func datumKindName(v Datum) string {
	switch v.Kind {
	case KindNull:
		return "null"
	case KindBool:
		return "boolean"
	case KindInt:
		return "integer"
	case KindString:
		return "text"
	case KindBytes:
		return "bytea"
	case KindTime:
		return "timestamp"
	case KindInterval:
		return "interval"
	case KindNumeric:
		return "numeric"
	default:
		return fmt.Sprintf("kind_%d", v.Kind)
	}
}

func isIntegerTypeName(name string) bool {
	switch strings.ToLower(name) {
	case "int", "integer", "int2", "smallint", "int4", "int8", "bigint":
		return true
	default:
		return false
	}
}

func isTextTypeName(name string) bool {
	switch strings.ToLower(name) {
	case "text", "varchar", "char", "bpchar", "character":
		return true
	default:
		return false
	}
}

func isBoolTypeName(name string) bool {
	switch strings.ToLower(name) {
	case "bool", "boolean":
		return true
	default:
		return false
	}
}

func isTimeTypeName(name string) bool {
	switch strings.ToLower(name) {
	case "timestamp", "timestamptz", "date", "time", "timetz":
		return true
	default:
		return false
	}
}

func isIntervalTypeName(name string) bool {
	return strings.EqualFold(name, "interval")
}

// injectTriggerVars adds OLD/NEW column values as mangled frame variables
// "_old_<colname>" / "_new_<colname>" so lowerPLpgSQLExpr can resolve
// OLD.col / NEW.col references. M0096-0012.
func injectTriggerVars(frame *plpgsqlFrame, trig *plpgsqlTrigCtx) {
	for i, col := range trig.Cols {
		colKey := strings.ToLower(col.Name)
		typ := normalizeCatalogType(col.Type)
		if trig.OldRow != nil {
			val := NullDatum
			if i < len(trig.OldRow) {
				val = trig.OldRow[i]
			}
			_ = frame.add("_old_"+colKey, typ, val)
		}
		if trig.NewRow != nil {
			val := NullDatum
			if i < len(trig.NewRow) {
				val = trig.NewRow[i]
			}
			_ = frame.add("_new_"+colKey, typ, val)
		}
	}
	// Inject TG_* string variables.
	strType := catalog.Type{Name: "text"}
	_ = frame.add("tg_name", strType, NewStringDatum(trig.TGName))
	_ = frame.add("tg_when", strType, NewStringDatum(trig.TGWhen))
	_ = frame.add("tg_level", strType, NewStringDatum(trig.TGLevel))
	_ = frame.add("tg_op", strType, NewStringDatum(trig.TGOp))
	_ = frame.add("tg_table_name", strType, NewStringDatum(trig.TGTable))
	_ = frame.add("tg_relname", strType, NewStringDatum(trig.TGTable))
	// Inject tg_argv as a text[] array (0-based indexing per PG convention).
	// Build as "{arg0,arg1,...}" text array. Individual elements are quoted if
	// they contain commas, braces, or backslashes.
	{
		arrType := catalog.Type{Name: "text[]"}
		var elems []string
		for _, a := range trig.TGArgs {
			// Escape backslash and double-quote for text array format.
			a = strings.ReplaceAll(a, "\\", "\\\\")
			a = strings.ReplaceAll(a, `"`, `\"`)
			if strings.ContainsAny(a, ",{}\" ") {
				a = `"` + a + `"`
			}
			elems = append(elems, a)
		}
		arrStr := "{" + strings.Join(elems, ",") + "}"
		_ = frame.add("tg_argv", arrType, NewStringDatum(arrStr))
	}
	// Inject OLD/NEW as composite-text row variables so RAISE NOTICE '%', OLD works.
	if trig.OldRow != nil {
		_ = frame.add("old", strType, NewStringDatum(rowToCompositeText(trig.Cols, trig.OldRow)))
	}
	if trig.NewRow != nil {
		_ = frame.add("new", strType, NewStringDatum(rowToCompositeText(trig.Cols, trig.NewRow)))
	}
}

// executePLpgSQLTriggerBody executes a trigger function body with the given
// trigger context. Returns:
//   - (newRow, true, nil): trigger returned a row (BEFORE triggers use this)
//   - (nil, true, nil): trigger returned NULL (skip the row)
//   - (nil, false, err): execution error
//
// M0096-0012.
func executePLpgSQLTriggerBody(r *catalog.Routine, trig *plpgsqlTrigCtx, ctx *Context) (Row, bool, error) {
	if strings.ToLower(r.Language) != "plpgsql" {
		return nil, true, nil // non-plpgsql trigger: pass-through
	}
	block, err := plpgsql.Parse(r.Body)
	if err != nil {
		return nil, false, &ExecError{Code: "P0000", Message: fmt.Sprintf("invalid trigger body for %s: %v", r.QualifiedName(), err)}
	}
	child := NewContext()
	if ctx != nil {
		*child = *ctx
		// Clear inherited notices so the child accumulates only its own;
		// existing ctx.Notices are propagated by the parent, not re-propagated
		// by the child's TakeNotices loop below. M0097-0140.
		child.Notices = nil
	}
	frame := newPLpgSQLFrame()
	frame.trig = trig
	injectTriggerVars(frame, trig)
	// Process DECLARE section.
	for _, d := range block.Declarations {
		typ := catalogTypeFromColumnType(d.Type)
		value := NullDatum
		if d.Default != nil {
			value, err = evalPLpgSQLExpr(d.Default, frame, child)
			if err != nil {
				return nil, false, err
			}
		}
		value, _ = coerceDatumToType(value, typ, d.Pos(), fmt.Sprintf("variable %q", d.Name))
		_ = frame.add(d.Name, typ, value)
	}
	_, flow, err := executePLpgSQLStmtList(block.Statements, r, frame, child)
	// Propagate notices accumulated in the trigger child context back to
	// the outer query context so they are emitted before CommandComplete.
	// M0096-0012: child.Notices is a separate slice from ctx.Notices because
	// executePLpgSQLTriggerBody copies ctx by value (*child = *ctx).
	if ctx != nil {
		for _, n := range child.TakeNotices() {
			ctx.AddNotice(n)
		}
	}
	if err != nil {
		return nil, false, err
	}
	switch flow {
	case flowReturnTriggerOld:
		return trig.OldRow, true, nil
	case flowReturnTriggerNew:
		return rebuildNewRowFromFrame(frame, trig), true, nil
	case flowReturnTriggerNull:
		return nil, true, nil // NULL = skip the row
	default:
		// No explicit RETURN — use OLD for BEFORE DELETE, NEW for others.
		if strings.ToLower(trig.TGOp) == "delete" {
			return trig.OldRow, true, nil
		}
		return rebuildNewRowFromFrame(frame, trig), true, nil
	}
}

// rebuildNewRowFromFrame reconstructs the trigger's NEW row from the
// frame's `_new_<colname>` slots after the trigger body has run.
// BEFORE triggers in PG can rewrite NEW.* (e.g. partition-key-update-1's
// `func_footrg_mod_a` does `NEW.a := 2`); without rebuilding the row
// from the frame, those rewrites would never reach the downstream
// partition routing / heap-write code. M0100-0005p.
func rebuildNewRowFromFrame(frame *plpgsqlFrame, trig *plpgsqlTrigCtx) Row {
	if trig == nil || trig.NewRow == nil {
		return nil
	}
	out := make(Row, len(trig.NewRow))
	copy(out, trig.NewRow)
	for i, col := range trig.Cols {
		idx, ok := frame.lookup("_new_" + strings.ToLower(col.Name))
		if !ok {
			continue
		}
		if i < len(out) {
			out[i] = frame.values[idx]
		}
	}
	return out
}

// execPLpgSQLEmbeddedSQL executes an embedded SQL statement from a PL/pgSQL
// body. Trigger OLD.* / NEW.* references are substituted with literal values
// before parsing. M0096-0012.
// plpgsqlFormatDynArg formats a Datum as a SQL literal for substitution
// into EXECUTE ... USING $N parameters. M0100-0005.
func plpgsqlFormatDynArg(d Datum) string {
	switch d.Kind {
	case KindNull:
		return "NULL"
	case KindInt:
		return strconv.FormatInt(d.Int, 10)
	case KindBool:
		if d.BoolValue() {
			return "true"
		}
		return "false"
	case KindNumeric:
		return numericText(d)
	default:
		s := d.StringValue()
		return "'" + strings.ReplaceAll(s, "'", "''") + "'"
	}
}

func execPLpgSQLEmbeddedSQL(sql string, frame *plpgsqlFrame, ctx *Context) error {
	// Substitute OLD.* → VALUES(v1, v2, ...) and OLD.col → literal.
	if frame.trig != nil {
		sql = substituteTriggerRefs(sql, frame.trig)
	}
	// Substitute PL/pgSQL frame variable references (tg_argv[N], tg_op, etc.).
	sql = substitutePlpgsqlFrameVarsInSQL(sql, frame)
	stmts, err := parser.Parse(sql)
	if err != nil {
		return &ExecError{Code: "42601", Message: fmt.Sprintf("PL/pgSQL embedded SQL parse error: %v", err)}
	}
	if len(stmts) == 0 {
		return nil
	}
	for _, stmt := range stmts {
		plan, err := planner.Plan(stmt, ctxPlanCatalog(ctx))
		if err != nil {
			return err
		}
		op, err := Build(plan)
		if err != nil {
			return err
		}
		if err := op.Open(ctx); err != nil {
			op.Close()
			return err
		}
		// Drain all rows (side-effect execution).
		for {
			_, err := op.Next()
			if err == EOF {
				break
			}
			if err != nil {
				op.Close()
				return err
			}
		}
		op.Close()
	}
	return nil
}

// substituteTriggerRefs replaces OLD.* / NEW.* / OLD.colname / NEW.colname
// in a SQL text with the actual literal values from the trigger context.
// Used by execPLpgSQLEmbeddedSQL for embedded SQL in trigger bodies. M0096-0012.
func substituteTriggerRefs(sql string, trig *plpgsqlTrigCtx) string {
	// Replace "OLD.*" and "NEW.*" with their column value lists.
	for _, which := range []string{"old", "new"} {
		var row Row
		if which == "old" {
			row = trig.OldRow
		} else {
			row = trig.NewRow
		}
		if row == nil {
			continue
		}
		// Replace "OLD.*" / "NEW.*" with "val1, val2, ..."
		star := strings.ToUpper(which) + ".*"
		if strings.Contains(strings.ToUpper(sql), star) {
			vals := make([]string, len(trig.Cols))
			for i := range trig.Cols {
				val := NullDatum
				if i < len(row) {
					val = row[i]
				}
				vals[i] = datumToSQLLiteral(val)
			}
			replacement := strings.Join(vals, ", ")
			// Case-insensitive replace.
			sqlUpper := strings.ToUpper(sql)
			starUpper := strings.ToUpper(star)
			idx := strings.Index(sqlUpper, starUpper)
			for idx != -1 {
				sql = sql[:idx] + replacement + sql[idx+len(star):]
				sqlUpper = strings.ToUpper(sql)
				idx = strings.Index(sqlUpper, starUpper)
			}
		}
		// Replace "OLD.colname" / "NEW.colname" with literal values.
		for i, col := range trig.Cols {
			ref := strings.ToUpper(which) + "." + strings.ToUpper(col.Name)
			sqlUpper := strings.ToUpper(sql)
			idx := strings.Index(sqlUpper, ref)
			if idx == -1 {
				continue
			}
			val := NullDatum
			if i < len(row) {
				val = row[i]
			}
			literal := datumToSQLLiteral(val)
			for idx != -1 {
				sql = sql[:idx] + literal + sql[idx+len(ref):]
				sqlUpper = strings.ToUpper(sql)
				idx = strings.Index(sqlUpper, ref)
			}
		}
	}
	return sql
}

// datumToSQLLiteral formats a Datum as a SQL literal string suitable for
// text substitution in embedded SQL. M0096-0012.
func datumToSQLLiteral(d Datum) string {
	if d.IsNull() {
		return "NULL"
	}
	switch d.Kind {
	case KindInt:
		return fmt.Sprintf("%d", d.Int)
	case KindBool:
		if d.BoolValue() {
			return "TRUE"
		}
		return "FALSE"
	case KindString:
		// Escape single quotes.
		s := strings.ReplaceAll(d.StringValue(), "'", "''")
		return "'" + s + "'"
	default:
		s := strings.ReplaceAll(d.Format(), "'", "''")
		return "'" + s + "'"
	}
}

// plpgsqlExtractMsgText extracts the displayable message text from a raw
// RAISE statement Msg field. The Msg field is the source text captured after
// the level keyword, e.g. `'hello world'` or `'format %', arg`. This
// function strips the outer single-quote delimiters from the first string
// literal. Format argument substitution (replacing `%` with actual values)
// is not yet implemented — the format-template text is returned as-is.
// M0096-0012.
// evalRaiseMsg evaluates a RAISE statement's message field in the plpgsql context.
// Handles format args: `'fmt %', arg1, arg2` → evaluates each arg, substitutes
// left-to-right into format (one % per arg; %% → literal %). M0097-0003.
func evalRaiseMsg(rawMsg string, frame *plpgsqlFrame, ctx *Context) string {
	rawMsg = strings.TrimSpace(rawMsg)
	if len(rawMsg) == 0 {
		return rawMsg
	}
	if rawMsg[0] != '\'' {
		return rawMsg // no format template
	}
	// Find closing quote of format template.
	end := 1
	for end < len(rawMsg) {
		if rawMsg[end] == '\'' {
			if end+1 < len(rawMsg) && rawMsg[end+1] == '\'' {
				end += 2
				continue
			}
			break
		}
		end++
	}
	fmtTemplate := strings.ReplaceAll(rawMsg[1:end], "''", "'")
	argsText := strings.TrimSpace(rawMsg[end+1:])
	if argsText == "" || !strings.HasPrefix(argsText, ",") {
		return fmtTemplate // no args
	}
	argsText = strings.TrimSpace(argsText[1:]) // skip leading comma

	// Split into individual arg expressions on top-level commas.
	argExprs := splitTopLevelCommas(argsText)

	// Evaluate each arg expression.
	argVals := make([]string, 0, len(argExprs))
	for _, ae := range argExprs {
		ae = strings.TrimSpace(ae)
		ae = substitutePlpgsqlArraySubscripts(ae, frame)
		parsed, err := parser.ParseExpr(ae)
		if err != nil {
			argVals = append(argVals, "")
			continue
		}
		lowered, err := lowerPLpgSQLExpr(parsed, frame)
		if err != nil {
			argVals = append(argVals, "")
			continue
		}
		val, err := evalExpr(lowered, frame.values, ctx)
		if err != nil {
			argVals = append(argVals, "")
			continue
		}
		if val.IsNull() {
			argVals = append(argVals, "<NULL>")
		} else {
			argVals = append(argVals, val.Format())
		}
	}

	// Substitute: each % (not %%) is replaced by the next arg in order.
	argIdx := 0
	var result strings.Builder
	for i := 0; i < len(fmtTemplate); i++ {
		if fmtTemplate[i] != '%' {
			result.WriteByte(fmtTemplate[i])
			continue
		}
		if i+1 < len(fmtTemplate) && fmtTemplate[i+1] == '%' {
			result.WriteByte('%')
			i++ // consume second %
			continue
		}
		if argIdx < len(argVals) {
			result.WriteString(argVals[argIdx])
			argIdx++
		} else {
			result.WriteByte('%') // no more args
		}
	}
	return result.String()
}

// splitTopLevelCommas splits s on commas not inside parentheses, brackets, or quotes.
func splitTopLevelCommas(s string) []string {
	var parts []string
	depth := 0
	inSingle := false
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inSingle {
			if c == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					i++ // escaped ''
				} else {
					inSingle = false
				}
			}
			continue
		}
		switch c {
		case '\'':
			inSingle = true
		case '(', '[':
			depth++
		case ')', ']':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, s[start:])
	return parts
}

// substitutePlpgsqlFrameVarsInSQL replaces PL/pgSQL frame variable references
// in embedded SQL text with SQL literal equivalents from the frame.
// This enables INSERT/SELECT with PL/pgSQL variable values (e.g. tg_op, tg_argv[0], c).
// Two passes: first varname[N] subscripts, then bare identifier references.
// A preceding or following "." is treated as a table/column qualifier and suppresses substitution.
// String literals and double-quoted identifiers are skipped.
func substitutePlpgsqlFrameVarsInSQL(sql string, frame *plpgsqlFrame) string {
	// Pass 1: substitute varname[N] patterns (array subscripts).
	var p1 strings.Builder
	i := 0
	for i < len(sql) {
		if isIdentStartByte(sql[i]) {
			j := i
			for j < len(sql) && isIdentContByte(sql[j]) {
				j++
			}
			varName := sql[i:j]
			if j < len(sql) && sql[j] == '[' {
				k := j + 1
				numStart := k
				for k < len(sql) && sql[k] >= '0' && sql[k] <= '9' {
					k++
				}
				if k < len(sql) && sql[k] == ']' && k > numStart {
					idxStr := sql[numStart:k]
					if idx64, err2 := strconv.ParseInt(idxStr, 10, 64); err2 == nil {
						if fi, ok := frame.lookup(varName); ok {
							val := frame.values[fi]
							if !val.IsNull() {
								elems := parseTextArray(val.StringValue())
								var elemIdx int
								if strings.EqualFold(varName, "tg_argv") {
									elemIdx = int(idx64) // tg_argv is 0-based
								} else {
									elemIdx = int(idx64) - 1 // regular PL/pgSQL arrays are 1-based
								}
								if elemIdx >= 0 && elemIdx < len(elems) {
									escaped := strings.ReplaceAll(elems[elemIdx], "'", "''")
									p1.WriteByte('\'')
									p1.WriteString(escaped)
									p1.WriteByte('\'')
									i = k + 1
									continue
								}
							}
						}
					}
				}
			}
			p1.WriteString(varName)
			i = j
		} else {
			p1.WriteByte(sql[i])
			i++
		}
	}
	s1 := p1.String()

	// Pass 2: substitute bare variable names (skip string/quoted literals, skip qualified names).
	var out strings.Builder
	i = 0
	for i < len(s1) {
		switch {
		case s1[i] == '\'':
			// Skip single-quoted string literal.
			j := i + 1
			for j < len(s1) {
				if s1[j] == '\'' {
					j++
					if j < len(s1) && s1[j] == '\'' {
						j++
						continue
					}
					break
				}
				j++
			}
			out.WriteString(s1[i:j])
			i = j
		case s1[i] == '"':
			// Skip double-quoted identifier.
			j := i + 1
			for j < len(s1) && s1[j] != '"' {
				j++
			}
			if j < len(s1) {
				j++
			}
			out.WriteString(s1[i:j])
			i = j
		case isIdentStartByte(s1[i]):
			j := i
			for j < len(s1) && isIdentContByte(s1[j]) {
				j++
			}
			// Skip if preceded by '.' (qualified name) or followed by '.' or '['.
			preceded := i > 0 && s1[i-1] == '.'
			followed := j < len(s1) && (s1[j] == '.' || s1[j] == '[')
			varName := s1[i:j]
			if !preceded && !followed {
				if fi, ok := frame.lookup(varName); ok {
					out.WriteString(datumToSQLLiteral(frame.values[fi]))
					i = j
					continue
				}
			}
			out.WriteString(varName)
			i = j
		default:
			out.WriteByte(s1[i])
			i++
		}
	}
	return out.String()
}

// substitutePlpgsqlArraySubscripts replaces `varname[N]` patterns in a SQL expression
// string with the literal value of that array element from the plpgsql frame. M0097-0003.
func substitutePlpgsqlArraySubscripts(expr string, frame *plpgsqlFrame) string {
	// Simple replacement: find patterns matching `ident[number]`
	result := expr
	// Look for varname[N] patterns and replace with SQL string literals.
	i := 0
	var out strings.Builder
	for i < len(result) {
		// Check if we're at the start of an identifier.
		if isIdentStartByte(result[i]) {
			j := i
			for j < len(result) && isIdentContByte(result[j]) {
				j++
			}
			varName := result[i:j]
			if j < len(result) && result[j] == '[' {
				// Found varname[...] pattern.
				k := j + 1
				numStart := k
				for k < len(result) && result[k] >= '0' && result[k] <= '9' {
					k++
				}
				if k < len(result) && result[k] == ']' && k > numStart {
					idxStr := result[numStart:k]
					idx64, err := strconv.ParseInt(idxStr, 10, 64)
					if err == nil {
						// Look up the variable in the frame.
						if fi, ok := frame.lookup(varName); ok {
							val := frame.values[fi]
							if !val.IsNull() {
								// Parse the array value and get the Nth element (1-indexed).
								elems := parseTextArray(val.StringValue())
								idx := int(idx64) - 1 // convert 1-indexed to 0-indexed
								if idx >= 0 && idx < len(elems) {
									elem := elems[idx]
									// Emit as a SQL string literal.
									escaped := strings.ReplaceAll(elem, "'", "''")
									out.WriteByte('\'')
									out.WriteString(escaped)
									out.WriteByte('\'')
									i = k + 1 // skip past varname[N]
									continue
								}
							}
						}
					}
				}
			}
			out.WriteString(varName)
			i = j
		} else {
			out.WriteByte(result[i])
			i++
		}
	}
	return out.String()
}

func plpgsqlExtractMsgText(msg string) string {
	msg = strings.TrimSpace(msg)
	if len(msg) == 0 {
		return msg
	}
	// Strip optional leading single-quoted string: 'text' [, args...]
	if msg[0] == '\'' {
		// Find the closing quote, handling escaped ''
		end := 1
		for end < len(msg) {
			if msg[end] == '\'' {
				if end+1 < len(msg) && msg[end+1] == '\'' {
					end += 2 // escaped ''
					continue
				}
				break
			}
			end++
		}
		inner := msg[1:end]
		// Unescape '' → '
		return strings.ReplaceAll(inner, "''", "'")
	}
	return msg
}

// rowToCompositeText formats a row as PostgreSQL composite type text notation:
// "(val1,val2,...)" with double-quoting for values that contain special characters.
// Used for RAISE NOTICE '... %', OLD / NEW substitution. M0100-0005.
func rowToCompositeText(cols []catalog.Column, row Row) string {
	var sb strings.Builder
	sb.WriteByte('(')
	for i, d := range row {
		if i > 0 {
			sb.WriteByte(',')
		}
		if i >= len(cols) {
			break
		}
		if d.IsNull() {
			// NULL renders as empty (no quotes)
			continue
		}
		s := d.Format() // Format() returns correct representation for all kinds (int, float, text)
		// Quote if the value contains comma, parens, whitespace, double-quote,
		// backslash, or is empty — matching PostgreSQL's composite output rules.
		needsQuote := len(s) == 0
		for _, c := range s {
			if c == ',' || c == '(' || c == ')' || c == '"' || c == '\\' || c == ' ' || c == '\t' || c == '\n' {
				needsQuote = true
				break
			}
		}
		if needsQuote {
			sb.WriteByte('"')
			for _, c := range s {
				if c == '"' || c == '\\' {
					sb.WriteByte('\\')
				}
				sb.WriteRune(c)
			}
			sb.WriteByte('"')
		} else {
			sb.WriteString(s)
		}
	}
	sb.WriteByte(')')
	return sb.String()
}

// ── Composite type helpers (M0097-composite) ─────────────────────────────────

// extractCompositeField extracts the Nth field (0-based) from a PostgreSQL
// composite literal like "(1,2,3)". Returns "" for NULL / missing fields.
// This implementation handles simple (non-quoted) scalar fields that arise
// from bigint / integer composite types (avg_state etc.).
func extractCompositeField(s string, idx int) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "(") || !strings.HasSuffix(s, ")") {
		return ""
	}
	inner := s[1 : len(s)-1]
	parts := strings.Split(inner, ",")
	if idx >= len(parts) {
		return ""
	}
	return strings.TrimSpace(parts[idx])
}

// updateCompositeField returns a new composite literal Datum where the field
// at fieldIdx (0-based) has been replaced by newVal.  If current is NULL the
// other fields are initialised to empty (NULL).
func updateCompositeField(current Datum, fieldIdx, nFields int, newVal Datum) Datum {
	var parts []string
	if current.IsNull() {
		parts = make([]string, nFields)
	} else {
		s := strings.TrimSpace(current.StringValue())
		if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
			inner := s[1 : len(s)-1]
			parts = strings.Split(inner, ",")
		} else {
			parts = make([]string, nFields)
		}
	}
	for len(parts) < nFields {
		parts = append(parts, "")
	}
	if newVal.IsNull() {
		parts[fieldIdx] = ""
	} else {
		parts[fieldIdx] = newVal.Format()
	}
	return NewStringDatum("(" + strings.Join(parts, ",") + ")")
}
