package executor

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/plpgsql"
)

// plpgsqlFrame is the local variable frame for one routine call.
// Names are case-insensitive and map to row slots consumed by evalExpr.
type plpgsqlFrame struct {
	indexByName map[string]int
	types       []catalog.Type
	values      Row
	// trig is non-nil when this frame is for a trigger function body.
	// M0096-0012.
	trig *plpgsqlTrigCtx
}

// plpgsqlTrigCtx holds the trigger execution context injected into
// trigger function frames. M0096-0012.
type plpgsqlTrigCtx struct {
	OldRow  Row
	NewRow  Row
	Cols    []catalog.Column // table columns (both OLD and NEW use the same schema)
	TGName  string
	TGWhen  string // "before" or "after"
	TGOp    string // "insert", "update", "delete"
	TGLevel string // "row" or "statement"
	TGTable string
}

func newPLpgSQLFrame() *plpgsqlFrame {
	return &plpgsqlFrame{indexByName: make(map[string]int)}
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
	return executePLpgSQLRoutine(r, args, ctx, x.Pos())
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

func executePLpgSQLRoutine(r *catalog.Routine, args []Datum, ctx *Context, pos int) (Datum, error) {
	if strings.ToLower(r.Language) != "plpgsql" {
		return Datum{}, &ExecError{Code: "0A000", Pos: pos, Message: fmt.Sprintf("function language %q is not executable in v0", r.Language)}
	}
	block, err := plpgsql.Parse(r.Body)
	if err != nil {
		return Datum{}, &ExecError{Code: "P0000", Pos: pos, Message: fmt.Sprintf("invalid PL/pgSQL body for function %s: %v", r.QualifiedName(), err)}
	}
	child := NewContext()
	if ctx != nil {
		*child = *ctx
	}
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
			if err := frame.add(r.ArgNames[i], declared, coerced); err != nil {
				return Datum{}, &ExecError{Code: "42P13", Pos: pos, Message: err.Error()}
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
	}
	res, flow, err := executePLpgSQLStmtList(block.Statements, r, frame, child)
	if err != nil {
		return Datum{}, err
	}
	if flow == flowReturn {
		return res, nil
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
		// _plpgsql_noop is the silent discard target for trigger OLD.b = ...
		// expressions. M0096-0012.
		if s.Target == "_plpgsql_noop" {
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
			ctx.AddNotice(raiseMsgEval())
		}
		return Datum{}, flowNone, nil

	case *plpgsql.SQLStmt:
		// Execute embedded SQL with trigger OLD/NEW substitution. M0096-0012.
		if err := execPLpgSQLEmbeddedSQL(s.SQL, frame, ctx); err != nil {
			return Datum{}, flowNone, err
		}
		return Datum{}, flowNone, nil

	case *plpgsql.ForSelectStmt:
		// FOR rec IN query LOOP ... END LOOP — query cursor loop. M0097-0012.
		sql := s.SQL
		if frame.trig != nil {
			sql = substituteTriggerRefs(sql, frame.trig)
		}
		// Execute the query and collect rows.
		stmts, err := parser.Parse(sql)
		if err != nil {
			return Datum{}, flowNone, &ExecError{Code: "42601", Message: fmt.Sprintf("FOR query parse error: %v", err)}
		}
		if len(stmts) == 0 {
			return Datum{}, flowNone, nil
		}
		plan, err := planner.Plan(stmts[0], ctx.Catalog)
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
			// Bind row columns to the loop variable's sub-fields.
			// For each column in the slot, inject as _<var>_<colname> in frame.
			if slot != nil && slot.Schema() != nil {
				row := slotRow(slot)
				for i, sc := range slot.Schema() {
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
			if v.Scale == 0 {
				return Datum{Kind: KindInt, Int: v.NumericMantissaValue()}, nil
			}
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
		case KindString, KindStringArena:
			return v, nil
		case KindBytes, KindBytesArena:
			return NewStringDatum(string(v.BytesValue())), nil
		}
	case isBoolTypeName(tn):
		if v.Kind == KindBool {
			return v, nil
		}
	case isTimeTypeName(tn):
		if v.Kind == KindTime {
			return v, nil
		}
	case isIntervalTypeName(tn):
		if v.Kind == KindInterval {
			return v, nil
		}
	default:
		// Unmodelled types remain pass-through in v0, mirroring the
		// no-op cast behaviour in planner/executor.
		return v, nil
	}
	return Datum{}, &ExecError{Code: "42804", Pos: pos, Message: fmt.Sprintf("%s expects type %q but got %s", subject, typ.Name, datumKindName(v))}
}

func datumKindName(v Datum) string {
	switch v.Kind {
	case KindNull:
		return "null"
	case KindBool:
		return "boolean"
	case KindInt:
		return "integer"
	case KindString, KindStringArena:
		return "text"
	case KindBytes, KindBytesArena:
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
		return trig.NewRow, true, nil
	case flowReturnTriggerNull:
		return nil, true, nil // NULL = skip the row
	default:
		// No explicit RETURN — use OLD for BEFORE DELETE, NEW for others.
		if strings.ToLower(trig.TGOp) == "delete" {
			return trig.OldRow, true, nil
		}
		return trig.NewRow, true, nil
	}
}

// execPLpgSQLEmbeddedSQL executes an embedded SQL statement from a PL/pgSQL
// body. Trigger OLD.* / NEW.* references are substituted with literal values
// before parsing. M0096-0012.
func execPLpgSQLEmbeddedSQL(sql string, frame *plpgsqlFrame, ctx *Context) error {
	// Substitute OLD.* → VALUES(v1, v2, ...) and OLD.col → literal.
	if frame.trig != nil {
		sql = substituteTriggerRefs(sql, frame.trig)
	}
	stmts, err := parser.Parse(sql)
	if err != nil {
		return &ExecError{Code: "42601", Message: fmt.Sprintf("PL/pgSQL embedded SQL parse error: %v", err)}
	}
	if len(stmts) == 0 {
		return nil
	}
	for _, stmt := range stmts {
		plan, err := planner.Plan(stmt, ctx.Catalog)
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
// Handles format args: `'%', expr` → evaluates expr, substitutes into format. M0097-0003.
func evalRaiseMsg(rawMsg string, frame *plpgsqlFrame, ctx *Context) string {
	rawMsg = strings.TrimSpace(rawMsg)
	if len(rawMsg) == 0 {
		return rawMsg
	}
	// Try to split: 'format_template' , arg1, arg2, ...
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
	argsText = strings.TrimSpace(argsText[1:]) // skip comma

	// Evaluate the args expression in the plpgsql frame.
	// Preprocess: replace variable array subscripts like r[N] with literal values.
	argsText = substitutePlpgsqlArraySubscripts(argsText, frame)

	// Parse and evaluate the args expression as SQL.
	argsExpr, err := parser.ParseExpr(argsText)
	if err != nil {
		return fmtTemplate // fallback
	}
	lowered, err := lowerPLpgSQLExpr(argsExpr, frame)
	if err != nil {
		return fmtTemplate
	}
	val, err := evalExpr(lowered, frame.values, ctx)
	if err != nil || val.IsNull() {
		return fmtTemplate
	}
	// Apply format substitution: replace % with the evaluated arg value.
	// Use Format() rather than StringValue() so non-string kinds (int, float, etc.)
	// are converted to their text representation. M0097-0003.
	argStr := val.Format()
	result := strings.ReplaceAll(fmtTemplate, "%", argStr)
	return result
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
