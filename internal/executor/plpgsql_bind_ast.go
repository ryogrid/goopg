package executor

import (
	"reflect"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// PL/pgSQL variable binding over a parsed SQL tree (M0146-0072).
//
// PostgreSQL plans every PL/pgSQL expression as a query whose variable
// references are parameters (pl_exec.c exec_prepare_plan,
// plpgsql_param_ref), so a variable read inside a sublink — a scalar
// subquery, EXISTS, an IN subquery, a WITH clause and its CTE bodies — sees
// the variable's current value. goopg's SQL fallback for such expressions
// (evalScalarSubquery / evalExprViaSQL) planned the tree untouched, so any
// variable inside it failed `column "i" does not exist`. These helpers
// return a copy of the tree with every variable reference replaced by a
// typed literal of the variable's current value — the same binding the
// statement path's text substitution (substitutePlpgsqlFrameVarsInSQL)
// gives an embedded SQL statement, and with the same precedence: a frame
// variable wins over a column of the same name.
//
// The copy is copy-on-write: only nodes on a path to a replaced reference
// are cloned, so the routine's cached body AST is never modified.

// bindPlpgsqlFrameVarsInExpr returns e with the frame's variables bound.
func bindPlpgsqlFrameVarsInExpr(e parser.Expr, frame *plpgsqlFrame) parser.Expr {
	if e == nil || frame == nil {
		return e
	}
	nv, changed := bindFrameVarsValue(reflect.ValueOf(&e).Elem(), frame, 0)
	if !changed {
		return e
	}
	return nv.Interface().(parser.Expr)
}

// bindPlpgsqlFrameVarsInSelect returns s with the frame's variables bound.
func bindPlpgsqlFrameVarsInSelect(s *parser.SelectStmt, frame *plpgsqlFrame) *parser.SelectStmt {
	if s == nil || frame == nil {
		return s
	}
	nv, changed := bindFrameVarsValue(reflect.ValueOf(s), frame, 0)
	if !changed {
		return s
	}
	return nv.Interface().(*parser.SelectStmt)
}

var parserExprType = reflect.TypeOf((*parser.Expr)(nil)).Elem()

func bindFrameVarsValue(v reflect.Value, frame *plpgsqlFrame, depth int) (reflect.Value, bool) {
	if depth > 400 || !v.IsValid() {
		return v, false
	}
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return v, false
		}
		if v.Type() == parserExprType {
			if cr, ok := v.Interface().(*parser.ColumnRef); ok {
				if lit, ok := frameVarLiteral(cr, frame); ok {
					nv := reflect.New(v.Type()).Elem()
					nv.Set(reflect.ValueOf(lit))
					return nv, true
				}
				return v, false
			}
		}
		inner, changed := bindFrameVarsValue(v.Elem(), frame, depth+1)
		if !changed {
			return v, false
		}
		nv := reflect.New(v.Type()).Elem()
		nv.Set(inner)
		return nv, true
	case reflect.Ptr:
		if v.IsNil() {
			return v, false
		}
		inner, changed := bindFrameVarsValue(v.Elem(), frame, depth+1)
		if !changed {
			return v, false
		}
		np := reflect.New(v.Type().Elem())
		np.Elem().Set(inner)
		return np, true
	case reflect.Struct:
		var cp reflect.Value
		changed := false
		for i := 0; i < v.NumField(); i++ {
			if !v.Type().Field(i).IsExported() {
				continue
			}
			nf, ch := bindFrameVarsValue(v.Field(i), frame, depth+1)
			if !ch {
				continue
			}
			if !changed {
				cp = reflect.New(v.Type()).Elem()
				cp.Set(v)
				changed = true
			}
			cp.Field(i).Set(nf)
		}
		if !changed {
			return v, false
		}
		return cp, true
	case reflect.Slice:
		if v.IsNil() {
			return v, false
		}
		var ns reflect.Value
		changed := false
		for i := 0; i < v.Len(); i++ {
			ne, ch := bindFrameVarsValue(v.Index(i), frame, depth+1)
			if !ch {
				continue
			}
			if !changed {
				ns = reflect.MakeSlice(v.Type(), v.Len(), v.Len())
				reflect.Copy(ns, v)
				changed = true
			}
			ns.Index(i).Set(ne)
		}
		if !changed {
			return v, false
		}
		return ns, true
	}
	return v, false
}

// frameVarLiteral is the bound value for a column reference that names a
// frame variable: a bare `i`, or `r.f` for a record variable r.
func frameVarLiteral(cr *parser.ColumnRef, frame *plpgsqlFrame) (parser.Expr, bool) {
	if cr.Schema != "" || cr.Column == "" || cr.Column == "*" {
		return nil, false
	}
	if cr.Table == "" {
		fi, ok := frame.lookup(cr.Column)
		if !ok {
			return nil, false
		}
		return typedDatumLiteral(cr.Pos(), frame.values[fi], frame.types[fi]), true
	}
	if !frame.isRecordVar(cr.Table) {
		return nil, false
	}
	fi, ok := frame.lookup("_" + strings.ToLower(cr.Table) + "_" + strings.ToLower(cr.Column))
	if !ok {
		return nil, false
	}
	return typedDatumLiteral(cr.Pos(), frame.values[fi], frame.types[fi]), true
}

// typedDatumLiteral spells d as a literal of type t — PG's parameter of the
// variable's declared type. A record-typed value stays an untyped literal
// (an anonymous record has no input function).
func typedDatumLiteral(pos int, d Datum, t catalog.Type) parser.Expr {
	name := strings.ToLower(t.Name)
	untyped := name == "" || name == "unknown" || name == "record"
	var lit parser.Expr
	switch {
	case d.IsNull():
		lit = parser.NewNullConst(pos)
	case d.Kind == KindString:
		lit = parser.NewStringConst(pos, d.StringValue())
	case d.Kind == KindBool:
		lit = parser.NewStringConst(pos, strconv.FormatBool(d.BoolValue()))
	case d.Kind == KindInt && (untyped || !t.IsArray && isIntegerTypeName(name)):
		if untyped {
			return parser.NewIntegerConst(pos, d.Int)
		}
		lit = parser.NewStringConst(pos, strconv.FormatInt(d.Int, 10))
	default:
		lit = parser.NewStringConst(pos, d.Format())
	}
	if untyped {
		return lit
	}
	typeName := t.Name
	if t.IsArray {
		typeName += "[]"
	}
	return parser.NewCastExpr(pos, lit, parser.ObjectName{Name: typeName}, nil)
}
