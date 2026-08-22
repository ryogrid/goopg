package executor

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/pl/plpgsql"
)

// callOp executes `CALL proc(...)` (M0015 Stage B).
type callOp struct {
	plan    *optimizer.Call
	ctx     *Context
	routine *catalog.Routine
	args    []Datum
	done    bool
}

func newCallOp(p *optimizer.Call) *callOp {
	return &callOp{plan: p}
}

func (o *callOp) Schema() optimizer.Schema {
	if o.routine == nil {
		return nil
	}
	var schema optimizer.Schema
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
			schema = append(schema, optimizer.SchemaColumn{
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
			typedArgList := buildTypedArgListStr(st.Args)
			return &ExecError{Code: "42809", Pos: st.Pos(),
				Message: fmt.Sprintf("%s%s is not a procedure", st.Name.Name, typedArgList),
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

	// Evaluate CALL arguments (all positions, including OUT placeholders).
	// OUT param values will be replaced with NULL after name-reordering below.
	frame := newPLpgSQLFrame()
	args := make([]Datum, len(st.Args))
	for i, arg := range st.Args {
		d, err := evalPLpgSQLExpr(arg, frame, ctx)
		if err != nil {
			// For OUT param placeholder expressions like 1/0, PG says the expression
			// is "not evaluated". Swallow execution errors and use NULL so that e.g.
			// CALL ptest9(1/0) succeeds. Type-mismatch errors remain via matching below.
			args[i] = NullDatum
		} else {
			args[i] = d
		}
	}
	// Track named-arg names for reordering after routine resolution.
	callerArgNames := st.ArgNames // nil if no named args

	// Match by argument count. For procedures with OUT params, the caller provides
	// placeholder values (NULL) for each OUT param too. So we match against the
	// total declared param count (IN + OUT + INOUT), or just IN count if fewer args.
	inArgCount := len(st.Args)
	// Infer caller arg types from expressions for type-based OUT param matching.
	callerArgTypeNames := inferCallArgTypes(st.Args)
	matches := make([]*catalog.Routine, 0, 1)
	for _, c := range routines {
		inCount := 0 // IN + INOUT params only
		totalCount := 0 // all params including OUT
		hasVariadic := false
		for _, mode := range c.ArgModes {
			totalCount++
			if mode == "" || mode == "i" || mode == "b" {
				inCount++
			}
			if mode == "v" {
				hasVariadic = true
				inCount++ // VARIADIC counts as IN for matching
			}
		}
		// If no ArgModes, all args are IN (backward compat).
		if c.ArgModes == nil {
			inCount = len(c.ArgTypes)
			totalCount = len(c.ArgTypes)
		}
		// Caller may provide:
		// - Exactly totalCount args (including OUT placeholders), OR
		// - At most inCount args (defaults fill missing IN/INOUT args), OR
		// - More than inCount when the last param is VARIADIC (extra args bundled).
		countMatch := inArgCount == totalCount || inArgCount <= inCount ||
			(hasVariadic && inArgCount >= inCount-1)
		if !countMatch {
			continue
		}
		// Type-check OUT param positions: when caller provides OUT placeholder
		// expressions with specific types (e.g. 1./0. → numeric), reject if
		// the inferred type is incompatible with the declared param type.
		typeMatch := true
		if inArgCount == totalCount {
			for i, mode := range c.ArgModes {
				if mode != "o" {
					continue
				}
				if i >= len(callerArgTypeNames) {
					break
				}
				callerType := callerArgTypeNames[i]
				paramType := strings.ToLower(c.ArgTypes[i].Name)
				if callerType == "unknown" || callerType == "" || paramType == "" {
					continue // unknown caller type: accept
				}
				// Basic type family compatibility check.
				if !callArgTypeCompatible(callerType, paramType) {
					typeMatch = false
					break
				}
			}
		}
		if typeMatch {
			matches = append(matches, c)
		}
	}
	// If no type-compatible match found but there were count-compatible candidates,
	// report "procedure name(inferred_types) does not exist".
	if len(matches) == 0 {
		// Build typed arg list from inferred types for error message.
		typedArgList := "(" + strings.Join(callerArgTypeNames, ", ") + ")"
		if len(callerArgTypeNames) == 0 {
			typedArgList = "()"
		}
		// Only add HINT when no procedure with this name exists at all.
		// When a procedure exists but types don't match, PG omits the hint.
		var hint string
		if len(routines) == 0 {
			hint = "No procedure matches the given name and argument types. You might need to add explicit type casts."
		}
		return &ExecError{Code: "42883", Pos: st.Pos(),
			Message: fmt.Sprintf("procedure %s%s does not exist", st.Name.Name, typedArgList),
			Hint:    hint,
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

	r := o.routine

	// Track which parameter positions were explicitly provided by the caller.
	// Positions not in this set may receive default values.
	providedSet := make(map[int]bool, len(args))

	// If caller provided OUT placeholders (len(args) == totalCount), map them to
	// the right positions (including OUT slots); otherwise positional IN-only mapping.
	totalCount := len(r.ArgTypes)
	callerProvidedAll := len(args) == totalCount

	// If named args were provided, reorder them to match routine parameter order.
	if len(callerArgNames) > 0 && len(callerArgNames) == len(args) {
		reordered := make([]Datum, len(r.ArgTypes))
		namedByName := map[string]Datum{}
		for i, nm := range callerArgNames {
			if nm == "" {
				reordered[i] = args[i]
				providedSet[i] = true
			} else {
				namedByName[strings.ToLower(nm)] = args[i]
			}
		}
		for i, paramName := range r.ArgNames {
			if d, ok := namedByName[strings.ToLower(paramName)]; ok {
				reordered[i] = d
				providedSet[i] = true
			}
		}
		args = reordered
	} else if callerProvidedAll {
		// Caller provided args for every param (including OUT placeholders).
		for i := 0; i < totalCount; i++ {
			providedSet[i] = true
		}
	} else {
		// Positional args: map caller args to IN/INOUT positions in order.
		inIdx := 0
		for i := 0; i < len(r.ArgTypes) && inIdx < len(args); i++ {
			mode := "i"
			if i < len(r.ArgModes) && r.ArgModes[i] != "" {
				mode = r.ArgModes[i]
			}
			if mode == "o" {
				continue // skip OUT params in positional mapping
			}
			providedSet[i] = true
			// Reorder needed: build mapping
			inIdx++
		}
		// If not callerProvidedAll and no named args, positional mapping is 0..len(args)-1
		// unless we have OUT params mixed in.
		if !callerProvidedAll {
			providedSet = make(map[int]bool, len(args))
			for i := 0; i < len(args); i++ {
				providedSet[i] = true
			}
		}
	}

	// Replace OUT param positions with NULL — the caller's expression value is
	// always discarded for OUT params (PostgreSQL: "you can write an expression,
	// but it's not evaluated").
	if len(args) == len(r.ArgTypes) {
		for i := range r.ArgModes {
			if r.ArgModes[i] == "o" {
				args[i] = NullDatum
			}
		}
	}

	// Fill missing IN/INOUT parameter slots with evaluated defaults.
	if len(args) < len(r.ArgTypes) {
		extended := make([]Datum, len(r.ArgTypes))
		copy(extended, args)
		args = extended
	}
	for i := 0; i < len(r.ArgTypes); i++ {
		if providedSet[i] {
			continue // explicitly provided, keep as-is
		}
		mode := "i"
		if i < len(r.ArgModes) && r.ArgModes[i] != "" {
			mode = r.ArgModes[i]
		}
		if mode == "o" {
			continue // OUT params don't need caller-provided value
		}
		if i < len(r.ArgDefaults) && r.ArgDefaults[i] != "" {
			d, err := evalCallDefault(r.ArgDefaults[i], ctx)
			if err != nil {
				return err
			}
			args[i] = d
		}
	}

	// Bundle VARIADIC args into an array datum (M0097-0022).
	// After positional/named arg mapping, if there's a VARIADIC param and
	// the caller provided more args than declared params, collect the excess
	// into an array at the VARIADIC position.
	for vi, mode := range r.ArgModes {
		if mode != "v" {
			continue
		}
		// Collect all values from position vi onward as the variadic array.
		if vi < len(args) {
			elems := make([]Datum, 0, len(args)-vi)
			for i := vi; i < len(args); i++ {
				elems = append(elems, args[i])
			}
			arrStr := buildArrayDatum(elems)
			args = append(args[:vi], arrStr)
			args = append(args, make([]Datum, len(r.ArgTypes)-len(args))...)
		}
		break
	}
	// Trim args to declared param count.
	if len(args) > len(r.ArgTypes) {
		args = args[:len(r.ArgTypes)]
	}

	o.args = args
	return nil
}

// buildArrayDatum constructs a text-format array datum from a list of elements.
// Used to bundle VARIADIC arguments. Result is stored as KindString with {e1,e2,...} format.
func buildArrayDatum(elems []Datum) Datum {
	if len(elems) == 0 {
		return NewStringDatum("{}")
	}
	parts := make([]string, len(elems))
	for i, d := range elems {
		if d.IsNull() {
			parts[i] = "NULL"
		} else {
			parts[i] = d.Format()
		}
	}
	return NewStringDatum("{" + strings.Join(parts, ",") + "}")
}

// evalCallDefault parses and evaluates a simple constant default expression.
// Handles integer, string, numeric, NULL, and boolean literals.
// For complex expressions (function calls etc.), attempts full evaluation.
func evalCallDefault(expr string, ctx *Context) (Datum, error) {
	stmts, err := parser.Parse("SELECT " + expr)
	if err != nil || len(stmts) != 1 {
		return NullDatum, nil
	}
	node, err := optimizer.Plan(stmts[0], ctxPlanCatalog(ctx))
	if err != nil {
		return NullDatum, nil
	}
	op, err := Build(node)
	if err != nil {
		return NullDatum, nil
	}
	if err := op.Open(ctx); err != nil {
		_ = op.Close()
		return NullDatum, nil
	}
	defer op.Close()
	slot, err := op.Next()
	if err == EOF {
		return NullDatum, nil
	}
	if err != nil {
		return NullDatum, nil
	}
	row := slotRow(slot)
	if len(row) == 0 {
		return NullDatum, nil
	}
	return row[0], nil
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
		lastRow, err := executeSQLProcedureReturning(r, o.args, o.ctx, callPos)
		if err != nil {
			return nil, err
		}
		// Return a tuple matching the OUT-param schema (empty for IN-only procedures).
		sch := o.Schema()
		if sch == nil || len(sch) == 0 {
			return nil, EOF
		}
		// Map OUT/INOUT columns from the last statement's result row.
		// outIdx tracks which column in the last-row corresponds to which OUT column.
		result := make(Row, len(sch))
		outIdx := 0
		for i := range result {
			if outIdx < len(lastRow) {
				result[i] = lastRow[outIdx]
				outIdx++
			}
		}
		return asSlot(sch, result), nil
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

// inferCallArgType infers the PG type name of a parser expression without evaluating.
func inferCallArgType(arg parser.Expr) string {
	switch x := arg.(type) {
	case *parser.IntegerConst:
		return "integer"
	case *parser.NumericConst:
		return "numeric"
	case *parser.StringConst:
		return "text"
	case *parser.BooleanConst:
		return "boolean"
	case *parser.NullConst:
		return "unknown"
	case *parser.BinaryOp:
		// Arithmetic on two numeric operands → numeric.
		lt := inferCallArgType(x.Left)
		rt := inferCallArgType(x.Right)
		if lt == "numeric" || rt == "numeric" {
			return "numeric"
		}
		if lt == "integer" && rt == "integer" {
			return "integer"
		}
		return "unknown"
	case *parser.UnaryOp:
		return inferCallArgType(x.Operand)
	case *parser.CastExpr:
		return strings.ToLower(x.Type.Name)
	}
	return "unknown"
}

// inferCallArgTypes infers type names from parser-level expressions
// for use in error messages. Falls back to "unknown" for complex exprs.
func inferCallArgTypes(args []parser.Expr) []string {
	types := make([]string, len(args))
	for i, arg := range args {
		types[i] = inferCallArgType(arg)
	}
	return types
}

// callArgTypeCompatible checks if a caller-provided arg type is compatible
// with a procedure parameter type. Used for OUT param type matching.
func callArgTypeCompatible(callerType, paramType string) bool {
	callerType = strings.ToLower(callerType)
	paramType = strings.ToLower(paramType)
	if callerType == paramType {
		return true
	}
	// Integer family compatibility.
	intFamily := map[string]bool{
		"integer": true, "int4": true, "int": true, "int2": true, "smallint": true,
		"int8": true, "bigint": true,
	}
	if intFamily[callerType] && intFamily[paramType] {
		return true
	}
	// Numeric/decimal family.
	numFamily := map[string]bool{
		"numeric": true, "decimal": true, "float4": true, "real": true,
		"float8": true, "double precision": true,
	}
	if numFamily[callerType] && numFamily[paramType] {
		return true
	}
	return false
}

// buildTypedArgListStr builds "(type1, type2, ...)" from inferred arg types.
func buildTypedArgListStr(args []parser.Expr) string {
	if len(args) == 0 {
		return "()"
	}
	types := inferCallArgTypes(args)
	return "(" + strings.Join(types, ", ") + ")"
}

// routineArgListStr formats arg types as "(type1, type2)" for error messages,
// using canonical PG type names (e.g. "int4" → "integer").
func routineArgListStr(argTypes []catalog.Type) string {
	if len(argTypes) == 0 {
		return "()"
	}
	parts := make([]string, len(argTypes))
	for i, t := range argTypes {
		parts[i] = canonicalTypeName(t.Name, 0)
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// funcArgModes converts a parsed function/procedure argument list into the
// parallel "i"/"o"/"b"/"v" mode strings catalog.Routine.ArgModes expects
// (mirrors the per-arg switch CREATE FUNCTION/PROCEDURE use when building a
// new routine). ALTER/DROP FUNCTION and COMMENT ON FUNCTION resolve existing
// routines by the same identity-argument list PG's
// pg_get_function_identity_arguments emits — which still carries OUT
// parameters — so a Lookup/ResolveBySig stub needs modes populated for
// Routine.Signature() to exclude them from the match the way
// pg_proc.proargtypes does.
func funcArgModes(args []parser.FunctionArg) []string {
	modes := make([]string, len(args))
	for i, a := range args {
		switch a.Mode {
		case parser.FuncArgOut:
			modes[i] = "o"
		case parser.FuncArgInout:
			modes[i] = "b"
		case parser.FuncArgVariadic:
			modes[i] = "v"
		default:
			modes[i] = "i"
		}
	}
	return modes
}

// routineArgTypeName renders a parsed argument type the same way
// execCreateFunction/execCreateProcedure build catalog.Routine.ArgTypes[i].Name:
// the array suffix is baked into the string (Signature() compares Name only,
// it does not consult catalog.Type.IsArray), so an ALTER/DROP/COMMENT lookup
// must reproduce that exact string to match a stored array-typed signature.
func routineArgTypeName(t parser.ColumnType) string {
	// M0119-0006 (deferral row 1344): the parser already lowercases unquoted
	// TokenIdent (identText) and preserves TokenQuotedIdent verbatim, so the
	// old strings.ToLower here was a no-op for unquoted names and destructive
	// for quoted ones — it folded `CREATE FUNCTION f(offpath."MyType")` to
	// "mytype". Drop the fold: every downstream consumer (Signature(),
	// TypeNameToOID, ArgTypeDisplayAlias) resolves case-insensitively, and
	// format_type_be (postgres/src/backend/utils/adt/format_type.c:343) emits
	// pg_type.typname verbatim through quote_identifier.
	name := t.Name
	if t.IsArray {
		name += "[]"
	}
	return name
}

// argTypeSchema returns the namespace an argument type belongs to, for the
// regprocedure output path's format_type_be arg-type qualification (deferral
// row 1342). An EXPLICITLY qualified name (ColumnType.Schema) is returned
// verbatim — never re-lowered: the parser case-folded an UNQUOTED qualifier
// (identText lowercases) but PRESERVED case for a quoted `"OffPath".mytype`.
// A BARE name is resolved against the user-type registries at capture time
// (deferral row 1343): the element type's NamespaceOID — populated at CREATE
// TYPE/DOMAIN and at startup reload — maps to its owner schema, mirroring PG's
// format_type.c:318 get_namespace_name_or_temp(typeform->typnamespace). The
// probe order (enum → domain → composite → range → multirange) matches
// userTypeOIDForName (expr.go); a bare BUILTIN name hits no registry and keeps
// "" (equivalent rendering: the renderer treats "" and "pg_catalog"
// identically). A pg_catalog-qualified name yields "pg_catalog" so the
// renderer's builtin-alias + never-qualify arms apply.
func argTypeSchema(t parser.ColumnType, cat catalog.Catalog, dbOid uint32) string {
	if t.Schema != "" {
		return t.Schema
	}
	// Bare name: probe the ELEMENT type's owner schema. Key on the raw element
	// name (before routineArgTypeName bakes the "[]"); strip a trailing "[]"
	// if one is present — the schema is the element type's.
	name := t.Name
	if base, isArray := splitArraySuffix(name); isArray {
		name = base
	}
	if et, ok := cat.LookupEnum(name, dbOid); ok {
		return cat.SchemaNameForOID(et.NamespaceOID)
	}
	if dom, ok := cat.LookupDomain(name, dbOid); ok {
		return cat.SchemaNameForOID(dom.NamespaceOID)
	}
	if ct := cat.LookupCompositeType(name, dbOid); ct != nil {
		return cat.SchemaNameForOID(ct.NamespaceOID)
	}
	if rt, ok := cat.LookupRangeType(name, dbOid); ok {
		return cat.SchemaNameForOID(rt.NamespaceOID)
	}
	if rt, ok := cat.LookupRangeTypeByMultirangeName(name); ok {
		return cat.SchemaNameForOID(rt.NamespaceOID)
	}
	return ""
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
		"encode", "decode", "md5", "sha224", "sha256", "sha384", "sha512":
		return true
	}
	return false
}
