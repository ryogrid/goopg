package executor

import (
	"fmt"
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
	for _, stmt := range block.Statements {
		switch s := stmt.(type) {
		case *plpgsql.AssignStmt:
			idx, ok := frame.lookup(s.Target)
			if !ok {
				return Datum{}, &ExecError{Code: "42703", Pos: s.Pos(), Message: fmt.Sprintf("variable %q does not exist", s.Target)}
			}
			v, err := evalPLpgSQLExpr(s.Value, frame, child)
			if err != nil {
				return Datum{}, err
			}
			v, err = coerceDatumToType(v, frame.types[idx], s.Pos(), fmt.Sprintf("variable %q", s.Target))
			if err != nil {
				return Datum{}, err
			}
			frame.values[idx] = v
		case *plpgsql.ReturnStmt:
			v, err := evalPLpgSQLExpr(s.Expr, frame, child)
			if err != nil {
				return Datum{}, err
			}
			v, err = coerceDatumToType(v, r.ReturnType, s.Pos(), "RETURN")
			if err != nil {
				return Datum{}, err
			}
			return v, nil
		default:
			return Datum{}, &ExecError{Code: "0A000", Pos: stmt.Pos(), Message: fmt.Sprintf("unsupported PL/pgSQL statement %T", stmt)}
		}
	}
	return Datum{}, &ExecError{Code: "2F005", Pos: pos, Message: fmt.Sprintf("control reached end of function %s without RETURN", r.QualifiedName())}
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
			if v.NumericScale == 0 {
				return Datum{Kind: KindInt, Int: v.NumericMantissa}, nil
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
		case KindString:
			return v, nil
		case KindBytes:
			return Datum{Kind: KindString, String: string(v.Bytes)}, nil
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
